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

// AutomationStore est ce que les automatisations attendent de la base.
type AutomationStore interface {
	ListReactions(ctx context.Context, projectID int64) ([]reaction.Reaction, error)
	GetReaction(ctx context.Context, projectID, id int64) (reaction.Reaction, error)
	SaveReaction(ctx context.Context, entry reaction.Reaction, actor string) (int64, error)
	DeleteReaction(ctx context.Context, projectID, id int64) error
	ReleaseAutomation(ctx context.Context, projectID int64) (store.ReleaseAutomation, error)
	SaveReleaseAutomation(ctx context.Context, entry store.ReleaseAutomation, actor string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// AutomationsHandler expose les réactions et le catalogue de l'éditeur.
type AutomationsHandler struct {
	store   AutomationStore
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func NewAutomationsHandler(
	reactions AutomationStore, pipelines *catalog.Catalog, logger *slog.Logger,
) *AutomationsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationsHandler{store: reactions, catalog: pipelines, logger: logger}
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
func (h *AutomationsHandler) List(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	reactions, err := h.store.ListReactions(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des réactions impossible", "")
		return
	}

	live, err := h.liveEventTypes(r.Context(), project.ID)
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
func (h *AutomationsHandler) Save(w http.ResponseWriter, r *http.Request) {
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
func (h *AutomationsHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	// Produces énumère ce que le pipeline laisse derrière lui, dans le vocabulaire
	// du catalogue. Vide pour une action, qui ne produit rien d'observable.
	Produces []string `json:"produces,omitempty"`
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
func (h *AutomationsHandler) Catalogue(w http.ResponseWriter, r *http.Request) {
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
				// Les paramètres qu'un pipeline demande, avec leurs libellés. Ils
				// n'étaient pas rendus tant qu'un plan de release était composé en
				// Go : personne n'avait à lire un nom de paramètre. Déclarer la
				// construction renverse cela (doc 15 §15.5).
				Fields: pipelineFields(pipeline),
				// Ce que le pipeline produit, pour que l'éditeur sache dans quelle
				// étape il a le droit d'aller. Le serveur refuse de toute façon, mais
				// un formulaire qui laisse choisir puis refuse est un formulaire
				// qu'on apprend à contourner.
				Produces: produces(pipeline),
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"gestes": gestes,
		"when":   []string{string(plan.WhenSuccess), string(plan.WhenFailure), string(plan.WhenAlways)},
		// Les rôles d'étape, et ce que chacun exige de ses gestes. L'éditeur
		// présente deux blocs — « Construction », « Vérification » — et doit
		// pouvoir dire lequel accepte quoi sans coder la table.
		"roles": roleChoices(),
	})
}

// roleChoice décrit un rôle d'étape pour l'éditeur.
type roleChoice struct {
	Role string `json:"role"`
	// Label est le titre du bloc dans l'éditeur.
	Label string `json:"label"`
	// Requires est le produit qu'un geste doit déclarer pour y entrer.
	Requires string `json:"requires"`
	// Advances dit ce que la conclusion de cette étape fait à la release.
	Advances string `json:"advances"`
}

func roleChoices() []roleChoice {
	return []roleChoice{
		{
			Role: string(plan.RoleBuild), Label: "Construction",
			Requires: string(catalog.ProductImage),
			Advances: "la release passe en vérification, ou échoue",
		},
		{
			Role: string(plan.RoleTest), Label: "Vérification",
			Requires: string(catalog.ProductResults),
			Advances: "la release devient prête, ou échoue",
		},
	}
}

// pipelineFields rend les paramètres qu'une déclaration peut renseigner.
func pipelineFields(pipeline catalog.Pipeline) []fieldPayload {
	declared := pipeline.Declared()
	fields := make([]fieldPayload, 0, len(declared))
	for _, input := range declared {
		fields = append(fields, fieldPayload{
			Name: input.Name, Label: input.Label, Type: "text",
			Help: input.Help, Required: input.Required,
			Placeholder: input.Placeholder,
		})
	}
	return fields
}

func produces(pipeline catalog.Pipeline) []string {
	names := make([]string, 0, len(pipeline.Produces))
	for _, product := range pipeline.Produces {
		names = append(names, string(product))
	}
	return names
}

// coreEventChoices énumère les faits du noyau, avec leurs champs appariables.
//
// Écrits ici et non déduits : les faits du noyau n'ont pas de descripteur — ils
// sont émis par du code, pas déclarés par un plugin — et leur donner un registre
// pour la seule commodité de cette liste ajouterait une seconde vérité à tenir
// alignée.
func coreEventChoices() []eventChoice {
	return []eventChoice{
		{Type: string(event.VersionCreated), Label: "Version déclarée dans le suivi",
			Fields: []string{"version_id", "name", "status", "tool", "external_id", "due_date"}},
		{Type: string(event.ReleaseCreated), Label: "Release déclarée",
			Fields: []string{"release_id", "tag", "kind", "git_ref", "version"}},
		{Type: string(event.ReleaseReleased), Label: "Release validée",
			Fields: []string{"release_id", "tag", "released_by"}},
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
func (h *AutomationsHandler) checkRefs(ctx context.Context, entry reaction.Reaction) error {
	var problems []error

	live, err := h.liveEventTypes(ctx, entry.ProjectID)
	if err != nil {
		return err
	}
	if !slices.Contains(live, entry.EventType) {
		problems = append(problems, fmt.Errorf(
			"le fait %q n'est produit par aucun plugin actif de cette version (connus: %s)",
			entry.EventType, strings.Join(live, ", ")))
	}

	// Un rôle sur l'étape d'une **réaction** n'a pas de sens et serait ignoré :
	// ce sont les étapes du plan d'une release qui font avancer son état, et une
	// réaction ne vise aucune release en particulier.
	for index, stage := range entry.Plan.Stages {
		if stage.Role != "" {
			problems = append(problems, fmt.Errorf(
				"l'étape %d d'une réaction porte le rôle %q : un rôle ne fait avancer "+
					"que l'automatisation d'une release", index+1, stage.Role))
		}
	}

	problems = append(problems, h.checkPlanRefs(ctx, entry.Plan))
	return errors.Join(problems...)
}

// checkPlanRefs refuse un plan dont un geste ne désigne rien d'exécutable.
//
// Partagé par les réactions et l'automatisation d'une release : les deux portent
// la même structure, et deux contrôles divergeraient au premier ajout.
func (h *AutomationsHandler) checkPlanRefs(ctx context.Context, entry plan.Plan) error {
	var problems []error

	pipelines, actions := plan.Refs(entry)
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
	for _, stage := range entry.Stages {
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
// Ceux du noyau, plus ceux des plugins **actifs pour ce projet** : un fait dont le
// plugin est éteint ne sera jamais produit, et l'accepter donnerait une réaction
// qui ne part jamais sans qu'on sache pourquoi.
func (h *AutomationsHandler) liveEventTypes(ctx context.Context, projectID int64) ([]string, error) {
	settings, err := h.store.PluginSettingsAll(ctx)
	if err != nil {
		return nil, err
	}
	own, err := h.store.ProjectPluginSettings(ctx, projectID)
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
		if entry, declared := own[descriptor.ID]; declared && descriptor.PerProject && !entry.Enabled {
			continue
		}
		for _, spec := range descriptor.Events {
			types = append(types, descriptor.EventType(spec))
		}
	}
	return types, nil
}

func (h *AutomationsHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
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

func (h *AutomationsHandler) audit(r *http.Request, name, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, name, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", name, "target", target, "error", err)
	}
}
