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
// # Une action interne, et les autres viennent des plugins
//
// [Registry] n'en déclare qu'une : l'appel sortant. C'est ce qui reste quand on
// retire ce qui appartient à une frontière — poser un statut de commit est un
// geste de **forge**, et le déclarer ici ferait qu'ajouter une seconde forge
// demanderait de modifier ce paquet.
//
// La notification de messagerie n'est pas une seconde action : Mattermost, Slack
// et le reste sont des webhooks, et leur donner une action dédiée serait trois
// fois le même code sous trois noms.
//
// [All] réunit les deux registres. C'est cette liste-là que voient l'éditeur et
// la validation ; la distinction entre une action du noyau et une action de
// plugin ne les regarde pas.
//
// Ce qui n'y est toujours pas et qu'il vaut mieux nommer : écrire dans le suivi.
// Le transport de `internal/redmine` refuse structurellement toute méthode autre
// que GET et HEAD, et c'est un choix documenté — pris parce que la clé visée est
// celle de la production du client. L'ouvrir est un chantier à part.
package action

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
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
	SecretRef bool `json:"secret_ref,omitempty"`
	// OneOf ferme le jeu des valeurs acceptées. Vide, la valeur est libre.
	OneOf       []string `json:"one_of,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// Descriptor déclare une action.
type Descriptor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Summary dit ce qu'elle fait, en une phrase, dans l'éditeur.
	Summary string  `json:"summary"`
	Fields  []Field `json:"fields"`
	// Plugin nomme le plugin qui fournit l'action, vide pour une action du noyau.
	//
	// Rendu à l'éditeur pour qu'il puisse dire d'où vient un geste, et lu par
	// l'exécution pour savoir à qui le confier.
	Plugin string `json:"plugin,omitempty"`
}

// Webhook émet un POST JSON vers une URL. Seule action du noyau.
const Webhook = "webhook"

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
	}

	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return descriptors
}

// All réunit les actions du noyau et celles que les plugins déclarent.
//
// # Le registre ouvert dans l'autre sens
//
// Les plugins déclaraient déjà les faits qu'ils observent ; ils déclarent
// maintenant les gestes qu'ils savent faire. Sans cette moitié, on branche
// n'importe quelle **cause** sur un jeu de **conséquences** figé, et agir sur une
// frontière que le noyau ne connaît pas redemande de modifier le noyau.
//
// # Sans filtrer sur l'activation
//
// Un plugin éteint garde ses actions dans cette liste. C'est délibéré : la
// validation d'un plan **figé** dans une exécution doit rester possible après
// extinction, sans quoi une vue d'historique perdrait ses libellés. C'est à
// l'éditeur d'écarter ce qui ne se déclencherait pas — il le fait déjà pour les
// faits, et pour la même raison.
func All() []Descriptor {
	descriptors := Registry()
	for _, provider := range plugin.Registry() {
		for _, spec := range provider.Actions {
			descriptors = append(descriptors, fromPlugin(provider, spec))
		}
	}

	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return descriptors
}

// fromPlugin traduit la déclaration d'un plugin en descripteur d'action.
//
// La traduction est étroite, et le champ qu'elle n'a pas à porter dit pourquoi :
// [Field.SecretRef] reste faux. Une action de plugin parle à sa propre frontière,
// dont la connexion est déjà scellée ; elle ne désigne aucun jeton, et ne peut
// donc pas servir à en faire sortir un vers une cible saisie dans un formulaire.
func fromPlugin(provider plugin.Descriptor, spec plugin.ActionSpec) Descriptor {
	fields := make([]Field, 0, len(spec.Fields))
	for _, field := range spec.Fields {
		fields = append(fields, Field{
			Name: field.Name, Label: field.Label, Help: field.Help,
			Required: field.Required, Template: field.Template,
			OneOf: field.OneOf, Placeholder: field.Placeholder,
		})
	}

	return Descriptor{
		ID: provider.ActionRef(spec), Label: spec.Label, Summary: spec.Summary,
		Fields: fields, Plugin: provider.ID,
	}
}

// ErrUnknown signale une action qu'aucun descripteur ne déclare.
var ErrUnknown = errors.New("action inconnue")

// Get rend le descripteur d'une action, du noyau ou d'un plugin.
//
// L'erreur nomme celles qui existent : chercher laquelle dans le code source
// n'est pas une réponse à donner à qui lit un message d'API.
func Get(id string) (Descriptor, error) {
	registry := All()
	for _, descriptor := range registry {
		if descriptor.ID == id {
			return descriptor, nil
		}
	}

	known := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		known = append(known, descriptor.ID)
	}
	return Descriptor{}, fmt.Errorf("%w %q (déclarées: %s)",
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
		problems = append(problems, field.validate(value)...)
	}

	return errors.Join(problems...)
}

func (f Field) validate(value string) []error {
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

	// Un jeu fermé se vérifie ici plutôt que chez le destinataire : l'état d'un
	// statut de commit appartient au vocabulaire de la forge, et une valeur
	// inventée serait refusée par elle — au moment le moins utile.
	//
	// La liste vient du descripteur et non d'une condition sur l'identifiant de
	// l'action : c'est ce qui permet à un second plugin de forge d'avoir ses
	// propres états sans que ce paquet ait à le connaître.
	//
	// Sauté quand la valeur porte un gabarit : ce qu'elle vaudra dépend de la
	// charge du fait, et refuser `{outcome}` ici obligerait à écrire l'état en dur.
	if len(f.OneOf) > 0 && !strings.Contains(value, "{") && !slices.Contains(f.OneOf, value) {
		problems = append(problems, fmt.Errorf(
			"%w: %s vaut %s, reçu %q",
			ErrInvalid, f.Label, strings.Join(f.OneOf, ", "), value))
	}

	// Un gabarit dont une accolade n'est pas fermée produirait une valeur
	// littérale incompréhensible chez le destinataire.
	if f.Template && strings.Count(value, "{") != strings.Count(value, "}") {
		problems = append(problems, fmt.Errorf(
			"%w: %s: accolades non appariées", ErrInvalid, f.Label))
	}

	return problems
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
