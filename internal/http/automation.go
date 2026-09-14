package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * L'automatisation d'une release : ce qu'un projet construit, puis vérifie.
 *
 * # Ce que cet écran remplace
 *
 * Trois endroits en dur, que doc 15 §15.1 nommait : une boucle sur la colonne
 * `targets` dans l'API, une boucle sur les familles du catalogue dans
 * l'observateur, et un drapeau `verification` qui disait l'enchaînement. Ici, un
 * plan — la même structure qu'une réaction, avec un rôle sur ses étapes.
 *
 * # Le rôle est vérifié, pas cru sur parole
 *
 * Une étape `build` dont un geste ne produit pas d'image est refusée. C'est le
 * contrôle qui empêche un changement silencieux de cycle de vie : ajouter une
 * action de notification à l'étape de construction ferait que la conclusion de
 * cette étape — donc le passage de la release en vérification — dépendrait d'un
 * webhook (doc 15 §15.3).
 *
 * Le catalogue déclare déjà `produces`. Le contrôle est une comparaison, pas une
 * heuristique.
 */

// automationPayload est ce que l'écran lit et écrit.
type automationPayload struct {
	Plan plan.Plan `json:"plan"`
	// Declared distingue « personne n'a rien saisi » de « quelqu'un a vidé ».
	//
	// L'écran ne peut pas le déduire d'un plan vide : les deux se présentent
	// identiquement, et seul le premier sera repris depuis les cibles du projet au
	// prochain démarrage du worker. Le dire évite d'annoncer « ce projet ne
	// construit plus » à quelqu'un dont l'automatisation est simplement en attente
	// de reprise.
	Declared  bool   `json:"declared"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// Automation rend l'automatisation d'un projet.
func (h *AutomationsHandler) Automation(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	entry, err := h.store.ReleaseAutomation(r.Context(), project.ID)
	switch {
	case errors.Is(err, store.ErrNoAutomation):
		// Un plan vide et `declared: false`. Ce n'est pas une 404 : le projet
		// existe, et la question « que construit-il ? » a une réponse — « rien de
		// déclaré pour l'instant ».
		writeJSON(w, http.StatusOK, automationPayload{Plan: plan.Plan{}, Declared: false})
		return
	case err != nil:
		h.logger.Error("lecture de l'automatisation", "projet", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Lecture impossible", "")
		return
	}

	writeJSON(w, http.StatusOK, automationPayload{
		Plan: entry.Plan, Declared: true, UpdatedBy: entry.UpdatedBy,
	})
}

type automationRequest struct {
	Plan plan.Plan `json:"plan"`
}

// SaveAutomation remplace l'automatisation d'un projet.
func (h *AutomationsHandler) SaveAutomation(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	var request automationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	// Un plan **vide** est accepté, et c'est délibéré : c'est la façon de dire
	// « ce projet ne construit plus ». `plan.Validate` le refuserait — une
	// réaction sans étape ne réagit à rien —, donc il n'est contrôlé que lorsqu'il
	// porte quelque chose.
	if len(request.Plan.Stages) > 0 {
		if err := request.Plan.Validate(); err != nil {
			writeProblem(w, http.StatusBadRequest, "Automatisation invalide", err.Error())
			return
		}
		if err := h.checkPlanRefs(r.Context(), request.Plan); err != nil {
			writeProblem(w, http.StatusBadRequest, "Automatisation invalide", err.Error())
			return
		}
		if err := h.checkRoles(request.Plan); err != nil {
			writeProblem(w, http.StatusBadRequest, "Automatisation invalide", err.Error())
			return
		}
	}

	identity, _ := IdentityFrom(r.Context())
	err := h.store.SaveReleaseAutomation(r.Context(), store.ReleaseAutomation{
		ProjectID: project.ID, Plan: request.Plan,
	}, identity.Subject)
	if err != nil {
		h.logger.Error("enregistrement de l'automatisation",
			"projet", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// Le journal porte le **compte** des étapes et des gestes, pas le plan : il
	// porte des gabarits, et un journal d'audit se lit avec moins de précautions
	// qu'une base. Le compte suffit à voir qu'une automatisation a été vidée.
	h.audit(r, "automation.save", project.Slug, map[string]any{
		"stages": len(request.Plan.Stages), "gestes": gesteCount(request.Plan),
	})

	writeJSON(w, http.StatusOK, automationPayload{Plan: request.Plan, Declared: true})
}

func gesteCount(p plan.Plan) int {
	total := 0
	for _, stage := range p.Stages {
		total += len(stage.Gestes)
	}
	return total
}

/*
 * Le contrôle des rôles.
 */

// roleProduct dit ce qu'un geste doit produire pour tenir un rôle.
//
// Une table et non un `switch` : elle se lit d'un coup, et le message d'erreur
// s'en sert pour nommer ce qu'il attendait.
var roleProduct = map[plan.Role]catalog.Product{
	plan.RoleBuild: catalog.ProductImage,
	plan.RoleTest:  catalog.ProductResults,
}

// checkRoles refuse une étape dont les gestes ne tiennent pas son rôle.
//
// Sans ce contrôle, le cycle de vie d'une release changerait par accident. Deux
// cas, et le second est le plus insidieux :
//
//   - un geste de test dans une étape `build` : la release passerait en
//     vérification dès que les tests concluent, c'est-à-dire après eux ;
//   - une **action** dans une étape à rôle : un webhook en échec ferait échouer
//     la release, alors qu'il n'a rien à dire de sa qualité.
func (h *AutomationsHandler) checkRoles(entry plan.Plan) error {
	var problems []error

	for index, stage := range entry.Stages {
		attendu, porte := roleProduct[stage.Role]
		if !porte {
			// Une étape sans rôle ne touche à rien : elle peut porter ce qu'elle
			// veut. C'est elle qui permet de prévenir une équipe sans mettre en jeu
			// l'état de la release.
			continue
		}

		for _, geste := range stage.Gestes {
			if geste.Kind != plan.GestePipeline {
				problems = append(problems, fmt.Errorf(
					"l'étape %d porte le rôle %q et le geste %q n'est pas un pipeline : "+
						"son échec ferait échouer la release sans rien dire de sa qualité",
					index+1, stage.Role, geste.Ref))
				continue
			}
			if h.catalog == nil {
				problems = append(problems, fmt.Errorf(
					"le rôle de l'étape %d ne peut pas être vérifié: aucun catalogue "+
						"n'est chargé", index+1))
				continue
			}
			pipeline, found := h.catalog.Find(geste.Ref)
			if !found {
				// Déjà signalé par le contrôle des références. Le redire ici
				// donnerait deux messages pour une seule faute.
				continue
			}
			if !pipeline.Yields(attendu) {
				problems = append(problems, fmt.Errorf(
					"l'étape %d porte le rôle %q et le pipeline %q ne produit pas %q "+
						"(il produit: %s)",
					index+1, stage.Role, geste.Ref, attendu,
					produitsDe(pipeline)))
			}
		}
	}

	return errors.Join(problems...)
}

// produitsDe nomme ce qu'un pipeline déclare produire, pour un message.
func produitsDe(pipeline catalog.Pipeline) string {
	if len(pipeline.Produces) == 0 {
		return "rien de déclaré"
	}
	names := make([]string, 0, len(pipeline.Produces))
	for _, product := range pipeline.Produces {
		names = append(names, string(product))
	}
	return strings.Join(names, ", ")
}
