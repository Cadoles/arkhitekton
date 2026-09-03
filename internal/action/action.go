// Package action déclare les actions qu'un plan peut mettre en œuvre.
//
// # Un registre, pas un moteur
//
// Ce paquet dit ce qu'une action **attend** et vérifie qu'on le lui donne. Il ne
// l'exécute pas : sortir du cluster demande un client contrôlé, qui vit dans
// `internal/webhook`, et le mélanger ici ferait de ce registre une dépendance de
// tout ce qui veut seulement lire la liste des actions — l'éditeur, notamment.
//
// C'est le même partage que pour `internal/plugin` : la déclaration d'un côté,
// l'exécution de l'autre.
//
// # Trois actions, et pas une de plus
//
// Elles sortent de ce qui a été demandé (doc 14 §14.3). La notification de
// messagerie n'en est pas une quatrième : Mattermost, Slack et le reste sont des
// webhooks, et leur donner une action dédiée serait trois fois le même code sous
// trois noms.
//
// Ce qui n'y est pas et qu'il vaut mieux nommer : écrire dans le suivi. Le
// transport de `internal/redmine` refuse structurellement toute méthode autre que
// GET et HEAD, et c'est un choix documenté — pris parce que la clé visée est celle
// de la production du client. L'ouvrir est un chantier à part.
package action

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Field déclare un paramètre attendu par une action.
//
// La même forme que les champs de `internal/plugin`, et pour la même raison :
// c'est ce qui permet à l'éditeur de composer un formulaire, et à la validation
// de refuser une saisie fautive à l'enregistrement plutôt qu'à l'exécution.
type Field struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Required : la valeur doit être là, éventuellement après interpolation du
	// contexte du fait.
	Required bool `json:"required,omitempty"`
	// Template dit que la valeur admet des variables `{clé}`, liées à la charge
	// du fait déclencheur.
	Template bool `json:"template,omitempty"`
	// SecretRef dit que la valeur **nomme** un champ sensible d'un plugin, et
	// n'en porte jamais le contenu.
	//
	// C'est la règle de doc 14 §14.7 rendue structurelle : un champ sensible n'est
	// jamais une variable de gabarit. Il ne peut être que désigné, et le moteur
	// le pose comme en-tête sans jamais le rendre lisible.
	SecretRef   bool   `json:"secret_ref,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// Descriptor déclare une action.
type Descriptor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Summary dit ce qu'elle fait, en une phrase, dans l'éditeur.
	Summary string  `json:"summary"`
	Fields  []Field `json:"fields"`
}

// Identifiants des actions internes.
const (
	// Webhook émet un POST JSON vers une URL.
	Webhook = "webhook"
	// ForgeStatus pose un statut de commit sur la forge.
	ForgeStatus = "forge.status"
)

// Registry énumère les actions internes.
//
// Une fonction et non une variable : rendre une tranche partagée laisserait un
// appelant la trier ou y écrire, et le registre suivant en hériterait.
func Registry() []Descriptor {
	descriptors := []Descriptor{
		{
			ID: Webhook, Label: "Appel sortant",
			Summary: "Émet un POST JSON vers une URL. C'est aussi la notification " +
				"de messagerie : Mattermost, Slack et les autres sont des webhooks.",
			Fields: []Field{
				{
					Name: "url", Label: "URL", Required: true,
					Help: "En HTTPS. Les cibles privées et la boucle locale sont " +
						"refusées, sauf autorisation explicite du déploiement.",
					Placeholder: "https://hooks.example.fr/services/…",
				},
				{
					Name: "body", Label: "Corps", Template: true,
					Help: "JSON, avec des variables `{tag}`, `{outcome}` liées à la " +
						"charge du fait. Vide, la charge entière est envoyée.",
				},
				{
					Name: "auth_header", Label: "En-tête d'authentification",
					Help:        "Nom de l'en-tête qui porte le jeton — `Authorization`.",
					Placeholder: "Authorization",
				},
				{
					Name: "auth_secret", Label: "Jeton", SecretRef: true,
					Help: "**Désigne** un champ sensible d'un plugin ; n'en porte " +
						"jamais le contenu. Le jeton est posé dans l'en-tête sans " +
						"passer par le corps ni par un journal.",
				},
			},
		},
		{
			ID: ForgeStatus, Label: "Statut de commit",
			Summary: "Pose un statut sur le commit construit, visible dans la forge " +
				"à côté de la demande de fusion.",
			Fields: []Field{
				{
					Name: "state", Label: "État", Required: true,
					Help:        "`pending`, `success`, `failure` ou `error`.",
					Placeholder: "success",
				},
				{
					Name: "context", Label: "Contexte", Required: true,
					Help:        "Le nom sous lequel le statut apparaît dans la forge.",
					Placeholder: "mse-dashboard/vérifications",
				},
				{
					Name: "description", Label: "Description", Template: true,
					Help: "Une ligne, avec des variables `{tag}`, `{outcome}`.",
				},
			},
		},
	}

	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return descriptors
}

// ErrUnknown signale une action qu'aucun descripteur ne déclare.
var ErrUnknown = errors.New("action inconnue")

// Get rend le descripteur d'une action.
//
// L'erreur nomme celles qui existent : chercher laquelle dans le code source
// n'est pas une réponse à donner à qui lit un message d'API.
func Get(id string) (Descriptor, error) {
	registry := Registry()
	for _, descriptor := range registry {
		if descriptor.ID == id {
			return descriptor, nil
		}
	}

	known := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		known = append(known, descriptor.ID)
	}
	return Descriptor{}, fmt.Errorf("%w %q (internes: %s)",
		ErrUnknown, id, strings.Join(known, ", "))
}

// ErrInvalid signale des paramètres qu'on refuse.
var ErrInvalid = errors.New("paramètres d'action invalides")

// Validate refuse des paramètres incohérents avec le descripteur.
//
// Vérifié à l'enregistrement d'un plan et non à son exécution : un geste fautif
// découvert au moment de partir laisse une chaîne arrêtée, et le diagnostic se
// fait alors sur une release en attente plutôt que devant un formulaire.
func (d Descriptor) Validate(params map[string]string) error {
	declared := make(map[string]Field, len(d.Fields))
	for _, field := range d.Fields {
		declared[field.Name] = field
	}

	var problems []error

	// Un paramètre inconnu est refusé plutôt qu'ignoré : ignoré, il donne un
	// écran qui dit « enregistré » sur une valeur que rien ne relira.
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, known := declared[name]; !known {
			problems = append(problems, fmt.Errorf(
				"%w: l'action %q ne déclare aucun paramètre %q", ErrInvalid, d.ID, name))
		}
	}

	for _, field := range d.Fields {
		value := strings.TrimSpace(params[field.Name])
		if field.Required && value == "" {
			problems = append(problems, fmt.Errorf("%w: %s est requis", ErrInvalid, field.Label))
			continue
		}
		if value == "" {
			continue
		}
		problems = append(problems, field.validate(d.ID, value)...)
	}

	return errors.Join(problems...)
}

func (f Field) validate(action, value string) []error {
	var problems []error

	// L'URL est vérifiée dans sa forme, pas dans sa cible : celle-ci est jugée au
	// moment de connecter, sur l'adresse résolue, par le transport. Les deux
	// contrôles ne se remplacent pas — celui-ci attrape une faute de frappe, le
	// second attrape une falsification.
	if f.Name == "url" {
		parsed, err := url.Parse(value)
		switch {
		case err != nil || parsed.Host == "":
			problems = append(problems, fmt.Errorf(
				"%w: %s doit être une URL absolue, reçu %q", ErrInvalid, f.Label, value))
		case parsed.Scheme != "https" && parsed.Scheme != "http":
			problems = append(problems, fmt.Errorf(
				"%w: %s: schéma %q (attendu https)", ErrInvalid, f.Label, parsed.Scheme))
		}
	}

	// L'état d'un statut de commit est un jeu fermé côté forge : une valeur
	// inventée serait refusée par elle, au moment le moins utile.
	if action == ForgeStatus && f.Name == "state" && !validStatusState(value) {
		problems = append(problems, fmt.Errorf(
			"%w: %s vaut pending, success, failure ou error, reçu %q",
			ErrInvalid, f.Label, value))
	}

	// Un gabarit dont une accolade n'est pas fermée produirait une valeur
	// littérale incompréhensible chez le destinataire.
	if f.Template && strings.Count(value, "{") != strings.Count(value, "}") {
		problems = append(problems, fmt.Errorf(
			"%w: %s: accolades non appariées", ErrInvalid, f.Label))
	}

	return problems
}

// StatusStates énumère les états qu'une forge accepte pour un statut de commit.
var StatusStates = []string{"pending", "success", "failure", "error"}

func validStatusState(value string) bool {
	return slices.Contains(StatusStates, value)
}

// Expand remplace les variables `{clé}` par la charge du fait.
//
// # Une accolade n'est pas toujours une variable
//
// Le corps d'un webhook est du **JSON**, et le JSON est fait d'accolades. Un
// balayage naïf — première accolade ouvrante, première fermante — ferait de
// `{"release":"{tag}"` un nom de variable, avalerait la vraie et rendrait le
// gabarit littéral. C'est le cas principal de cette fonction, pas un cas tordu.
//
// Ne sont donc reconnues comme variables que les accolades dont le contenu est un
// **nom plausible** : lettres, chiffres, `_`, `.`, `-`. Tout le reste — une
// accolade de structure JSON, un fragment — traverse tel quel.
//
// # Une variable inconnue reste littérale
//
// Plutôt que remplacée par du vide, et c'est délibéré : un `{tag}` visible dans
// un message de messagerie se remarque et se corrige, là qu'un trou silencieux se
// lit comme une release sans nom — et personne ne saurait dire si c'est le
// gabarit ou la donnée qui manque.
func Expand(template string, values map[string]string) string {
	if !strings.Contains(template, "{") {
		return template
	}

	var out strings.Builder
	out.Grow(len(template))

	for index := 0; index < len(template); {
		if template[index] != '{' {
			out.WriteByte(template[index])
			index++
			continue
		}

		end := strings.IndexByte(template[index:], '}')
		if end < 0 {
			// Plus aucune fermante : le reste est littéral.
			out.WriteString(template[index:])
			break
		}
		end += index

		name := template[index+1 : end]
		if !plausibleName(name) {
			// Une accolade de structure. On écrit celle-ci et on reprend juste
			// après : la fermante qu'on vient de voir appartient peut-être à une
			// vraie variable, et la consommer ici la ferait disparaître.
			out.WriteByte('{')
			index++
			continue
		}

		if value, known := values[name]; known {
			out.WriteString(value)
		} else {
			out.WriteString(template[index : end+1])
		}
		index = end + 1
	}

	return out.String()
}

// plausibleName dit si le contenu d'une paire d'accolades peut être un nom de
// variable.
//
// Volontairement étroit : mieux vaut laisser passer une accolade qui aurait pu
// être une variable — elle se voit et se corrige — que d'interpréter une
// structure JSON comme une interpolation, qui produirait une charge muette.
func plausibleName(name string) bool {
	if name == "" {
		return false
	}
	for _, letter := range name {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= 'A' && letter <= 'Z',
			letter >= '0' && letter <= '9',
			letter == '_', letter == '.', letter == '-':
		default:
			return false
		}
	}
	return true
}
