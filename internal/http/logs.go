package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
)

// LogReader est ce que les handlers attendent du lecteur de journaux.
type LogReader interface {
	Tail(ctx context.Context, runName, taskName string, length int64) (logs.Segment, error)
	From(ctx context.Context, runName, taskName string, offset, length int64) (logs.Segment, error)
	// LinesBefore numérote la fenêtre rendue dans le journal entier.
	LinesBefore(ctx context.Context, runName, taskName string, offset int64) (int64, error)
}

// logPollInterval espace deux relectures du stockage pendant un suivi.
//
// Plus court que l'intervalle de flush du collecteur serait du gaspillage : rien
// de neuf ne peut apparaître entre deux flushs.
const logPollInterval = 2 * time.Second

// LogsHandler sert les journaux, en lecture ponctuelle comme en suivi.
type LogsHandler struct {
	reader LogReader
}

func NewLogsHandler(reader LogReader) *LogsHandler {
	return &LogsHandler{reader: reader}
}

type logPayload struct {
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	// FirstLine est le rang, dans le journal entier, de la première ligne rendue.
	// Sans lui le client numérote à partir de ce qu'il a chargé, et « ligne 42 »
	// ne désigne pas la même chose selon la quantité lue — un lien partagé tombe
	// alors à côté.
	FirstLine int64 `json:"first_line"`
	// Sealed dit à l'interface qu'il est inutile de continuer à suivre : le
	// journal ne changera plus.
	Sealed bool `json:"sealed"`
}

// Read rend une portion de journal.
//
// Sans paramètre, la fin — c'est ce qu'on regarde en premier, l'échec est à la
// fin. Avec `offset`, la suite à partir de cette position, ce qui permet de
// remonter dans l'historique comme de poursuivre un suivi.
func (h *LogsHandler) Read(w http.ResponseWriter, r *http.Request) {
	runName := r.PathValue("run")
	taskName := r.PathValue("task")

	length := intQuery(r, "length", 0)

	var segment logs.Segment
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		segment, err = h.reader.From(r.Context(), runName, taskName, intQuery(r, "offset", 0), length)
	} else {
		segment, err = h.reader.Tail(r.Context(), runName, taskName, length)
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture du journal impossible", "")
		return
	}

	// Le comptage n'a lieu que pour la lecture d'une fenêtre, jamais pour le
	// suivi : le flux n'ajoute qu'à la fin, ce qui ne renumérote rien.
	firstLine, err := h.reader.LinesBefore(r.Context(), runName, taskName, segment.Offset)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture du journal impossible", "")
		return
	}

	writeJSON(w, http.StatusOK, logPayload{
		Content:    string(segment.Content),
		Offset:     segment.Offset,
		NextOffset: segment.NextOffset,
		Size:       segment.Size,
		FirstLine:  firstLine,
		Sealed:     segment.Sealed,
	})
}

// Stream suit un journal en direct.
//
// La reprise passe par `Last-Event-ID`, dont la valeur est le décalage en octets :
// un client qui se reconnecte reprend exactement où il s'était arrêté, sans
// doublon ni trou. C'est le mécanisme standard de SSE, et il tombe juste ici parce
// que le journal est une suite d'octets numérotée de façon stable.
func (h *LogsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	runName := r.PathValue("run")
	taskName := r.PathValue("task")

	stream, ok := newSSEWriter(w)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "Flux non supporté", "")
		return
	}

	// Sans reprise, le client reçoit d'abord la fin du journal : se brancher sur
	// un run en cours et ne rien voir avant la prochaine ligne serait déroutant.
	offset := int64(-1)

	// Le paramètre de requête sert à la **première** connexion : EventSource ne
	// permet pas de poser un en-tête, et un client qui a déjà chargé la fin par
	// l'API de lecture doit pouvoir démarrer le flux à sa suite plutôt que de
	// recevoir cette fin une seconde fois.
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Last-Event-ID l'emporte : il vient des reconnexions du navigateur, et sa
	// valeur est plus récente que celle inscrite dans l'URL au premier abonnement.
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		if parsed, err := strconv.ParseInt(resume, 10, 64); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		segment, err := h.next(r.Context(), runName, taskName, offset)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			_ = stream.event("error", "", "lecture du journal impossible")
			return
		}
		offset = segment.NextOffset

		if len(segment.Content) > 0 {
			if err := stream.event("log", strconv.FormatInt(segment.NextOffset, 10), string(segment.Content)); err != nil {
				return
			}
		}

		if segment.Sealed && offset >= segment.Size {
			// Le journal ne changera plus : l'annoncer permet au client de fermer
			// la connexion au lieu de sonder indéfiniment.
			_ = stream.event("sealed", strconv.FormatInt(segment.Size, 10), "")
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := stream.comment("battement"); err != nil {
				return
			}
		case <-ticker.C:
		}
	}
}

// Raw sert le journal entier en texte.
//
// Deux usages pour une seule route, distingués par `download` : ouvrir le
// journal brut dans un onglet — la visionneuse virtualise, ce qui empêche de
// chercher avec le navigateur ou de tout sélectionner — et l'enregistrer en
// fichier. La ressource est la même, seule la façon de la présenter change ;
// deux routes pour cela auraient dupliqué la lecture.
//
// Le contenu est rendu tel qu'il est stocké, horodatages du kubelet compris.
// C'est le sens de « brut » : ce que la visionneuse retire pour l'affichage doit
// se retrouver ici, sans quoi le fichier téléchargé ne permettrait plus de
// recouper avec un autre journal.
func (h *LogsHandler) Raw(w http.ResponseWriter, r *http.Request) {
	runName := r.PathValue("run")
	taskName := r.PathValue("task")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		// Le nom de fichier est composé des deux identifiants : un dossier de
		// téléchargements où dix fichiers s'appellent `log.txt` ne sert à rien.
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", sanitizeFilename(runName+"-"+taskName)+".txt"))
	}

	// La lecture est paginée parce que le lecteur borne chaque appel à MaxRead :
	// un journal d'intégration dépasse largement cette taille. Les tranches sont
	// écrites au fil de l'eau plutôt qu'accumulées — le journal complet n'a
	// aucune raison de tenir en mémoire du serveur avant de partir.
	var offset int64
	for {
		segment, err := h.reader.From(r.Context(), runName, taskName, offset, 0)
		if err != nil {
			if offset == 0 {
				writeProblem(w, http.StatusInternalServerError, "Lecture du journal impossible", "")
				return
			}
			// L'en-tête est parti et une partie du corps aussi : il n'y a plus de
			// code d'état à changer. Interrompre laisse une réponse tronquée, ce
			// qui est le moins mauvais des états possibles.
			return
		}

		if _, err := w.Write(segment.Content); err != nil {
			// Onglet fermé en cours de téléchargement : rien à signaler.
			return
		}

		// La progression est la condition d'arrêt, et pas seulement l'égalité à
		// la taille : un journal qui grossit pendant la lecture ferait tourner
		// cette boucle indéfiniment si l'on attendait de la rattraper.
		if segment.NextOffset <= offset || segment.NextOffset >= segment.Size {
			return
		}
		offset = segment.NextOffset
	}
}

// sanitizeFilename ne garde que ce qui traverse sans dommage un en-tête et un
// système de fichiers. Les noms de PipelineRun sont déjà des identifiants DNS,
// mais l'en-tête est construit ici et rien ne garantit qu'il le restera.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
}

func (h *LogsHandler) next(
	ctx context.Context, runName, taskName string, offset int64,
) (logs.Segment, error) {
	if offset < 0 {
		return h.reader.Tail(ctx, runName, taskName, 0)
	}
	return h.reader.From(ctx, runName, taskName, offset, 0)
}

func intQuery(r *http.Request, name string, fallback int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// Un paramètre d'affichage illisible retombe sur le défaut : refuser la
		// requête n'apprendrait rien à personne.
		return fallback
	}
	return value
}
