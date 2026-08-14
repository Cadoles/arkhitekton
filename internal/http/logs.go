package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
)

// LogReader est ce que les handlers attendent du lecteur de journaux.
type LogReader interface {
	Tail(ctx context.Context, runName, taskName string, length int64) (logs.Segment, error)
	From(ctx context.Context, runName, taskName string, offset, length int64) (logs.Segment, error)
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

	writeJSON(w, http.StatusOK, logPayload{
		Content:    string(segment.Content),
		Offset:     segment.Offset,
		NextOffset: segment.NextOffset,
		Size:       segment.Size,
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
