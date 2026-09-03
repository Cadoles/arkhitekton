package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/action"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/reaction"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les réactions déclarées, et le catalogue qui permet de les écrire.
 *
 * # Les références sont vérifiées ici
 *
 * `internal/reaction` porte la forme ; cette couche porte les **références** —
 * qu'un type de fait soit produit, qu'un geste désigne une famille du catalogue ou
 * une action du registre. C'est ici parce que c'est ici que les registres sont
 * sous la main.
 *
 * Vérifiées à l'enregistrement et non au déclenchement : une réaction fautive
 * découverte au moment où elle devait partir ne se manifeste que par une absence,
 * et l'absence se diagnostique mal — on cherche d'abord si le fait a été émis.
 *
 * # Le catalogue de l'éditeur
 *
 * L'écran ne code aucune liste. Il demande ce que cette version sait faire : les
 * faits qu'on peut écouter, avec les champs de leur charge ; les actions, avec
 * leurs paramètres ; les familles du catalogue. Une liste écrite dans l'interface
 * divergerait du binaire au premier ajout, et l'écart se verrait par un geste
 * qu'on peut choisir et que rien n'exécute.
 */

// ReactionStore est ce que les réactions attendent de la base.
type ReactionStore interface {
	ListReactions(ctx context.Context, projectID int64) ([]reaction.Reaction, error)
	GetReaction(ctx context.Context, projectID, id int64) (reaction.Reaction, error)
	SaveReaction(ctx context.Context, entry reaction.Reaction, actor string) (int64, error)
	DeleteReaction(ctx context.Context, projectID, id int64) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// ReactionsHandler expose les réactions et le catalogue de l'éditeur.
type ReactionsHandler struct {
	store   ReactionStore
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func NewReactionsHandler(
	reactions ReactionStore, pipelines *catalog.Catalog, logger *slog.Logger,
) *ReactionsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReactionsHandler{store: reactions, catalog: pipelines, logger: logger}
}

type reactionPayload struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	EventType string            `json:"event_type"`
	Enabled   bool              `json:"enabled"`
	Match     map[string]string `json:"match"`
	Plan      plan.Plan         `json:"plan"`
	// Inert dit que la réaction ne peut plus se déclencher : son fait n'est plus
	// produit par cette version, ou le plugin qui le produisait est éteint.
	//
	// Rendu plutôt que la réaction effacée : une extinction peut être temporaire,
	// et effacer silencieusement ferait perdre une configuration. L'écran
	// l'affiche inerte, en le disant.
	Inert  bool   `json:"inert,omitempty"`
	Reason string `json:"inert_reason,omitempty"`
}

// List rend les réactions d'un projet.
func (h *ReactionsHandler) List(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	reactions, err := h.store.ListReactions(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des réactions impossible", "")
		return
	}

	live, err := h.liveEventTypes(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins impossible", "")
		return
	}

	payload := make([]reactionPayload, 0, len(reactions))
	for _, entry := range reactions {
		view := reactionPayload{
			ID: entry.ID, Name: entry.Name, EventType: entry.EventType,
			Enabled: entry.Enabled, Match: orEmptyMap(entry.Match), Plan: entry.Plan,
		}
		if !slices.Contains(live, entry.EventType) {
			view.Inert = true
			view.Reason = "ce fait n'est plus produit : le plugin qui l'observe est " +
				"éteint, ou cette version ne le déclare plus"
		}
		payload = append(payload, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reactions": payload,
		"project":   project.Slug,
	})
}

type reactionRequest struct {
	Name      string            `json:"name"`
	EventType string            `json:"event_type"`
	Enabled   bool              `json:"enabled"`
	Match     map[string]string `json:"match"`
	Plan      plan.Plan         `json:"plan"`
}

// Save crée ou remplace une réaction.
func (h *ReactionsHandler) Save(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	var request reactionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	// L'identifiant vient du chemin et non du corps : un corps qui le porterait
	// permettrait de déplacer une réaction d'un projet à l'autre en modifiant deux
	// champs, et l'URL cesserait de dire ce qu'on écrit.
	var id int64
	if raw := strings.TrimSpace(r.PathValue("reaction")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
			return
		}
		id = parsed
	}

	entry := reaction.Reaction{
		ID: id, ProjectID: project.ID, Name: strings.TrimSpace(request.Name),
		EventType: strings.TrimSpace(request.EventType), Enabled: request.Enabled,
		Match: request.Match, Plan: request.Plan,
	}

	if err := entry.Validate(); err != nil {
		writeProblem(w, http.StatusBadRequest, "Réaction invalide", err.Error())
		return
	}
	if err := h.checkRefs(r.Context(), entry); err != nil {
		writeProblem(w, http.StatusBadRequest, "Réaction invalide", err.Error())
		return
	}

	identity, _ := IdentityFrom(r.Context())
	saved, err := h.store.SaveReaction(r.Context(), entry, identity.Subject)
	switch {
	case errors.Is(err, store.ErrReactionExists):
		writeProblem(w, http.StatusConflict, "Nom déjà pris",
			"une réaction de ce projet porte déjà ce nom")
		return
	case errors.Is(err, store.ErrReactionNotFound):
		writeProblem(w, http.StatusNotFound, "Réaction introuvable", "")
		return
	case err != nil:
		h.logger.Error("enregistrement d'une réaction",
			"projet", project.Slug, "nom", entry.Name, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// Le journal porte le fait écouté et l'activation. Pas le plan : il porte des
	// gabarits, et un journal d'audit se lit avec moins de précautions qu'une base.
	h.audit(r, "reaction.save", project.Slug+"/"+entry.Name, map[string]any{
		"event_type": entry.EventType, "enabled": entry.Enabled,
	})

	writeJSON(w, http.StatusOK, map[string]any{"id": saved})
}

// Delete retire une réaction.
func (h *ReactionsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("reaction")), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
		return
	}

	if err := h.store.DeleteReaction(r.Context(), project.ID, id); err != nil {
		if errors.Is(err, store.ErrReactionNotFound) {
			writeProblem(w, http.StatusNotFound, "Réaction introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Suppression impossible", "")
		return
	}

	h.audit(r, "reaction.delete", project.Slug+"/"+strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

/*
 * Le catalogue de l'éditeur.
 */

type eventChoice struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	// Source distingue le noyau d'un plugin. Vide pour le noyau.
	Source string `json:"source,omitempty"`
	// Fields énumère les champs appariables, et donc les variables de gabarit.
	Fields []string `json:"fields"`
}

type gesteChoice struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
	// Summary n'est renseigné que pour une action : une famille du catalogue se
	// décrit par son libellé.
	Summary string         `json:"summary,omitempty"`
	Fields  []fieldPayload `json:"fields,omitempty"`
	// Source nomme le plugin qui fournit le geste. Vide pour le noyau.
	//
	// Rendu pour la même raison que sur un fait : deux gestes peuvent porter le
	// même libellé — « Statut de commit » — et ne pas parler à la même forge.
	Source string `json:"source,omitempty"`
}

// Catalogue rend ce que cette version sait faire.
//
// L'écran ne code aucune liste : il demande, et compose son formulaire. Une liste
// écrite dans l'interface divergerait du binaire au premier ajout, et l'écart se
// verrait par un geste qu'on peut choisir et que rien n'exécute.
func (h *ReactionsHandler) Catalogue(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins impossible", "")
		return
	}

	events := coreEventChoices()
	for _, descriptor := range plugin.Registry() {
		// Un fait dont le plugin est éteint n'apparaît pas dans l'éditeur : un
		// choix qui ne se déclencherait jamais est pire qu'une absence.
		if !settings[descriptor.ID].Enabled {
			continue
		}
		for _, spec := range descriptor.Events {
			events = append(events, eventChoice{
				Type: descriptor.EventType(spec), Label: spec.Label,
				Source: descriptor.ID, Fields: fieldNames(spec.Payload),
			})
		}
	}

	gestes := make([]gesteChoice, 0, 8)
	for _, descriptor := range action.All() {
		// Une action dont le plugin est éteint n'apparaît pas, exactement comme un
		// fait qu'il n'observe plus : un geste qu'on peut choisir et qui refusera
		// de partir est pire qu'une absence.
		if descriptor.Plugin != "" && !settings[descriptor.Plugin].Enabled {
			continue
		}
		gestes = append(gestes, gesteChoice{
			Kind: string(plan.GesteAction), Ref: descriptor.ID,
			Label: descriptor.Label, Summary: descriptor.Summary,
			Fields: actionFields(descriptor), Source: descriptor.Plugin,
		})
	}
	if h.catalog != nil {
		for _, kind := range h.catalog.Kinds() {
			pipeline, found := h.catalog.Find(kind)
			if !found {
				continue
			}
			gestes = append(gestes, gesteChoice{
				Kind: string(plan.GestePipeline), Ref: pipeline.Kind, Label: pipeline.Label,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"gestes": gestes,
		"when":   []string{string(plan.WhenSuccess), string(plan.WhenFailure), string(plan.WhenAlways)},
	})
}

// coreEventChoices énumère les faits du noyau, avec leurs champs appariables.
//
// Écrits ici et non déduits : les faits du noyau n'ont pas de descripteur — ils
// sont émis par du code, pas déclarés par un plugin — et leur donner un registre
// pour la seule commodité de cette liste ajouterait une seconde vérité à tenir
// alignée.
func coreEventChoices() []eventChoice {
	return []eventChoice{
		{Type: string(event.ReleaseCreated), Label: "Release déclarée",
			Fields: []string{"release_id", "tag", "kind", "git_ref", "version"}},
		{Type: string(event.ReleaseBuilt), Label: "Construction conclue",
			Fields: []string{"release_id", "tag", "outcome", "job_id", "git_commit"}},
		{Type: string(event.ReleaseVerified), Label: "Vérifications conclues",
			Fields: []string{"release_id", "tag", "outcome", "kinds"}},
		{Type: string(event.ReleaseReleased), Label: "Release validée",
			Fields: []string{"release_id", "tag", "released_by"}},
		{Type: string(event.ReleasePromoted), Label: "Promotion aboutie",
			Fields: []string{"release_id", "tag", "environment", "job_id"}},
		{Type: string(event.PipelineFinished), Label: "Exécution terminée",
			Fields: []string{"run", "run_id", "kind", "outcome", "job_id", "release_id"}},
	}
}

func fieldNames(fields []plugin.Field) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.Name)
	}
	return names
}

func actionFields(descriptor action.Descriptor) []fieldPayload {
	fields := make([]fieldPayload, 0, len(descriptor.Fields))
	for _, field := range descriptor.Fields {
		fields = append(fields, fieldPayload{
			Name: field.Name, Label: field.Label, Type: "text", Help: field.Help,
			Required: field.Required, Secret: field.SecretRef,
			Options: field.OneOf, Placeholder: field.Placeholder,
		})
	}
	return fields
}

/*
 * Les contrôles de référence.
 */

// checkRefs refuse une réaction dont une référence ne désigne rien.
func (h *ReactionsHandler) checkRefs(ctx context.Context, entry reaction.Reaction) error {
	var problems []error

	live, err := h.liveEventTypes(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(live, entry.EventType) {
		problems = append(problems, fmt.Errorf(
			"le fait %q n'est produit par aucun plugin actif de cette version (connus: %s)",
			entry.EventType, strings.Join(live, ", ")))
	}

	pipelines, actions := entry.Refs()
	for _, ref := range pipelines {
		if h.catalog == nil {
			problems = append(problems, fmt.Errorf(
				"la famille %q ne peut pas être vérifiée: aucun catalogue n'est chargé", ref))
			continue
		}
		if _, found := h.catalog.Find(ref); !found {
			problems = append(problems, fmt.Errorf(
				"la famille %q est absente du catalogue (déclarées: %s)",
				ref, strings.Join(h.catalog.Kinds(), ", ")))
		}
	}
	// Les réglages sont relus ici plutôt que passés depuis `liveEventTypes` : ce
	// chemin est celui d'un enregistrement, pas d'un affichage, et une seconde
	// lecture y coûte moins qu'un paramètre de plus à faire circuler.
	settings, err := h.store.PluginSettingsAll(ctx)
	if err != nil {
		return err
	}
	for _, ref := range actions {
		descriptor, err := action.Get(ref)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		// Un geste dont le plugin est éteint est refusé à l'enregistrement, comme
		// un fait qu'aucun plugin actif ne produit. La symétrie n'est pas
		// esthétique : dans les deux cas la réaction serait déclarée, visible, et
		// sans effet — et c'est ce qu'on ne veut pas laisser croire.
		if descriptor.Plugin != "" && !settings[descriptor.Plugin].Enabled {
			problems = append(problems, fmt.Errorf(
				"le geste %q vient du plugin %q, qui est éteint pour cette instance : "+
					"il ne partirait pas", ref, descriptor.Plugin))
		}
	}

	// Les paramètres d'une action sont validés contre son descripteur : une URL
	// malformée ou un gabarit mal apparié se corrige devant le formulaire, pas
	// devant une chaîne arrêtée.
	for _, stage := range entry.Plan.Stages {
		for _, geste := range stage.Gestes {
			if geste.Kind != plan.GesteAction {
				continue
			}
			descriptor, err := action.Get(geste.Ref)
			if err != nil {
				continue
			}
			if err := descriptor.Validate(geste.Params); err != nil {
				problems = append(problems, err)
			}
		}
	}

	return errors.Join(problems...)
}

// liveEventTypes énumère les faits qu'un projet peut effectivement écouter.
//
// Ceux du noyau, plus ceux des plugins **actifs** : un fait dont le plugin est
// éteint ne sera jamais produit, et l'accepter donnerait une réaction qui ne part
// jamais sans qu'on sache pourquoi.
func (h *ReactionsHandler) liveEventTypes(ctx context.Context) ([]string, error) {
	settings, err := h.store.PluginSettingsAll(ctx)
	if err != nil {
		return nil, err
	}

	types := make([]string, 0, 12)
	for _, choice := range coreEventChoices() {
		types = append(types, choice.Type)
	}
	for _, descriptor := range plugin.Registry() {
		if !settings[descriptor.ID].Enabled {
			continue
		}
		for _, spec := range descriptor.Events {
			types = append(types, descriptor.EventType(spec))
		}
	}
	return types, nil
}

func (h *ReactionsHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	project, err := h.store.ProjectBySlug(r.Context(),
		strings.TrimSpace(r.PathValue("project")))
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet inconnu", "")
			return store.Project{}, false
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
		return store.Project{}, false
	}
	return project, true
}

func (h *ReactionsHandler) audit(r *http.Request, name, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, name, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", name, "target", target, "error", err)
	}
}
