// Package reaction porte la déclaration « sur tel fait, exécuter tel plan ».
//
// # Ce que ce paquet sait, et ce qu'il ignore
//
// Il connaît la **forme** d'une réaction et la façon dont elle apparie un fait. Il
// ignore ce que les références de ses gestes désignent : qu'une famille existe au
// catalogue, qu'une action soit déclarée au registre, qu'un type de fait soit
// produit par un plugin — trois questions dont la réponse vit ailleurs, et qui se
// posent à l'enregistrement, dans la couche qui a ces registres sous la main.
//
// La séparation est la même que pour `internal/plan` : la structure ici, les
// références au moment de les résoudre.
//
// # L'appariement reste pauvre, délibérément
//
// Égalité et appartenance, rien d'autre (doc 14 §14.2). Le jour où quelqu'un
// demande « si le score dépasse 80 et que le tag ne contient pas hotfix », la
// bonne réponse sera d'ajouter un opérateur nommé, pas un interpréteur : une
// configuration qui devient un programme est une configuration qu'on ne peut plus
// relire, et le dashboard n'offre aucun débogueur pour elle.
package reaction

import (
	"errors"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

// Reaction est la déclaration d'un enchaînement.
type Reaction struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"-"`
	Name      string `json:"name"`
	// EventType est le fait écouté : `release.created`, ou
	// `gitea.pull_request.opened` pour un fait de plugin.
	EventType string `json:"event_type"`
	Enabled   bool   `json:"enabled"`
	// Match apparie la charge du fait. Vide : la réaction part sur tout fait de
	// ce type.
	//
	// Une carte plate, comme la charge qu'elle apparie : un appariement porte sur
	// une valeur, pas sur un chemin. `$.release.kind` demanderait un langage de
	// chemins et l'interface qui va avec.
	Match map[string]string `json:"match,omitempty"`
	Plan  plan.Plan         `json:"plan"`
}

// ErrInvalid signale une réaction qu'on refuse d'enregistrer.
var ErrInvalid = errors.New("réaction invalide")

// maxNameLength borne le nom. Il sert à retrouver une réaction dans une liste, pas
// à la décrire.
const maxNameLength = 120

// Validate refuse une réaction qui ne pourrait pas se déclencher comme elle se
// lit.
//
// Vérifiée à l'enregistrement et non au premier fait : une déclaration fautive
// découverte au moment où elle devait partir ne se manifeste que par une absence,
// et l'absence se diagnostique mal — on cherche d'abord si le fait a été émis.
func (r Reaction) Validate() error {
	var problems []error

	if name := strings.TrimSpace(r.Name); name == "" {
		problems = append(problems, fmt.Errorf(
			"%w: un nom est requis — c'est par lui qu'on la retrouve dans une liste",
			ErrInvalid))
	} else if len(name) > maxNameLength {
		problems = append(problems, fmt.Errorf(
			"%w: nom de %d caractères, borné à %d", ErrInvalid, len(name), maxNameLength))
	}

	if strings.TrimSpace(r.EventType) == "" {
		problems = append(problems, fmt.Errorf(
			"%w: aucun fait écouté", ErrInvalid))
	}

	for key, value := range r.Match {
		if strings.TrimSpace(key) == "" {
			problems = append(problems, fmt.Errorf(
				"%w: un appariement porte sur un champ sans nom", ErrInvalid))
		}
		// Une valeur vide apparierait un champ absent de la charge, ce que la
		// règle refuse par ailleurs. La laisser passer donnerait une réaction qui
		// ne part jamais sans qu'on sache pourquoi.
		if strings.TrimSpace(value) == "" {
			problems = append(problems, fmt.Errorf(
				"%w: l'appariement sur %q n'a aucune valeur", ErrInvalid, key))
		}
	}

	if err := r.Plan.Validate(); err != nil {
		problems = append(problems, err)
	}

	return errors.Join(problems...)
}

// Matches dit si la charge d'un fait satisfait l'appariement.
//
// **Toutes** les clés déclarées doivent correspondre : un appariement est une
// conjonction. Deux conditions qui se satisferaient l'une ou l'autre s'écrivent
// en deux réactions, ce qui se lit mieux qu'un opérateur de plus.
//
// Une clé absente de la charge **ne correspond pas**. Le contraire ferait partir
// une réaction sur un fait qui ne porte pas ce dont elle parle — le cas exact
// d'un `environment` attendu sur un fait qui n'en a pas.
func (r Reaction) Matches(payload map[string]string) bool {
	for key, expected := range r.Match {
		value, present := payload[key]
		if !present || !oneOf(value, expected) {
			return false
		}
	}
	return true
}

// oneOf dit si une valeur figure dans une liste séparée par des virgules.
//
// L'appartenance est écrite ainsi plutôt qu'avec un opérateur dédié : `preprod,
// production` se saisit dans un champ de texte et se relit sans documentation,
// là où une syntaxe d'ensemble demanderait d'en apprendre une.
func oneOf(value, expected string) bool {
	for _, candidate := range strings.Split(expected, ",") {
		if strings.TrimSpace(candidate) == value {
			return true
		}
	}
	return false
}

// Refs énumère ce que les gestes du plan désignent, par nature.
//
// Sert à la couche qui a les registres sous la main : elle vérifie que chaque
// référence existe **à l'enregistrement**, plutôt que de laisser la découverte au
// moment où le geste devait partir.
func (r Reaction) Refs() (pipelines, actions []string) {
	// Délégué au plan depuis que l'automatisation d'une release en porte un aussi
	// (doc 15 §15.5) : la question « que désigne ce plan ? » n'appartient pas plus
	// à une réaction qu'à une automatisation.
	return plan.Refs(r.Plan)
}

// Trigger nomme le déclencheur d'une exécution issue de cette réaction.
//
// `reaction:<id>`, en regard du `builtin:<nom>` de la chaîne de vérification. Les
// deux partagent la même unicité par fait, et c'est ce qui permet à une réaction
// déclarée de coexister avec la chaîne intégrée sans que l'une bloque l'autre.
func (r Reaction) Trigger() string {
	return fmt.Sprintf("reaction:%d", r.ID)
}
