// Package plugin déclare les extensions du dashboard et ce que chacune attend
// comme configuration.
//
// # Un registre à la compilation, et rien d'autre
//
// Doc 12 §12.5 tranche le mécanisme : un plugin est un paquet, une installation
// est une reconstruction. Pas de `plugin` Go, pas de gRPC hors-process, pas de
// chargement à l'exécution. Ce paquet ne contient donc **aucun** `init()` et
// aucune résolution dynamique : il énumère ce que ce binaire sait faire.
//
// Ce que le registre ajoute par rapport à `internal/provider` : celui-ci nomme le
// **contrat** qu'une frontière honore — ce qu'on peut demander à un suivi. Celui-
// ci nomme les **réalisations** disponibles et ce qu'il faut leur fournir pour
// qu'elles fonctionnent. L'un sert au compilateur, l'autre à l'écran de réglages.
//
// # Deux portées, et pourquoi les deux
//
// Un plugin sert d'abord les spécificités d'un projet, ce qui plaide pour une
// configuration par projet. Mais certaines valeurs sont celles de l'instance —
// l'URL de la forge, le namespace de Flux — et les redemander à chaque projet
// serait une saisie répétée dont la moindre divergence se paierait en pannes
// obscures.
//
// D'où la règle : le global porte les **valeurs par défaut**, le projet porte ses
// **écarts**. Un champ vide au niveau d'un projet reprend celui de l'instance, et
// c'est ce qui permet à un déploiement mono-client de ne rien saisir deux fois
// tout en laissant un second client déclarer son propre serveur.
//
// # Les champs sensibles
//
// Un champ marqué [Field.Secret] n'est jamais rendu par l'API, et n'est jamais
// écrit en clair : il est scellé par `internal/secrets`. L'écran n'en montre que
// la présence. C'est ce qui rend la saisie d'une clé d'API acceptable depuis une
// interface web — sans quoi la relire suffirait à la voler.
package plugin

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"
)

// Kind est le point d'extension qu'un plugin réalise.
//
// Jeu **fermé** : en ajouter un demande du code, pas de la configuration. Tekton n'y figure pas,
// délibérément — c'est une hypothèse assumée du produit, comme PostgreSQL.
type Kind string

const (
	// KindTracker suit les versions, les demandes et leur avancement.
	KindTracker Kind = "tracker"
	// KindForge porte les dépôts, les branches et les tags.
	KindForge Kind = "forge"
	// KindExporter produit les livrables contractuels.
	KindExporter Kind = "exporter"
	// KindModule apporte un domaine entier : ses écrans, ses routes, ses boucles.
	//
	// Il remplace `deployer` et `attester`, qui n'avaient qu'une réalisation
	// chacun — Flux et PortalData — et n'existent que dans le contexte MSE. Un
	// plugin qui déploie, atteste et apporte un écran n'entrait dans aucune de ces
	// cases ; le déclarer « déployeur » passerait le reste sous silence (doc 16).
	KindModule Kind = "module"
)

// ModuleMSE identifie le plugin MSE : environnements, promotion par Flux,
// attestation par PortalData.
const ModuleMSE = "mse"

// KindLabel nomme chaque point d'extension en français, pour l'interface.
var KindLabel = map[Kind]string{
	KindTracker:  "Suivi",
	KindForge:    "Forge",
	KindExporter: "Livrables",
	KindModule:   "Module",
}

// FieldType dit comment un champ se saisit et se vérifie.
type FieldType string

const (
	FieldText FieldType = "text"
	FieldURL  FieldType = "url"
	FieldBool FieldType = "bool"
	// FieldList est une liste de valeurs, saisie séparée par des virgules et
	// stockée telle quelle. Le format suit celui des variables d'environnement
	// qu'elle remplace : une configuration migrée se recopie sans traduction.
	FieldList FieldType = "list"
)

// Field déclare un réglage attendu par un plugin.
type Field struct {
	Name  string    `json:"name"`
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
	// Help dit à quoi sert le champ, et se lit sous lui. Pas décoratif : c'est ce
	// qui évite qu'un identifiant de projet soit rempli avec son libellé.
	Help string `json:"help,omitempty"`
	// Required vaut pour la portée où le champ est déclaré. Un champ obligatoire
	// au niveau d'un projet peut rester vide s'il est renseigné globalement :
	// c'est la valeur **résolue** qui doit exister, pas la valeur saisie.
	Required bool `json:"required,omitempty"`
	// Secret marque une valeur scellée en base et jamais rendue.
	Secret      bool   `json:"secret,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// EventSpec déclare un fait qu'un plugin sait observer.
//
// C'est la moitié manquante de l'extensibilité : sans elle, on branche n'importe
// quelle **conséquence** sur un jeu de **causes** figé, et réagir à un fait que le
// noyau ne produit pas demande à nouveau de modifier le noyau (doc 14 §14.2).
type EventSpec struct {
	// Suffix est le type **sans** son préfixe : `pull_request.opened`.
	//
	// Déclaré nu et préfixé par [Descriptor.EventType] : un plugin qui écrirait
	// son type entier pourrait oublier le préfixe, et deux plugins de forge
	// produiraient alors des faits indiscernables.
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
	// Payload énumère les champs de la charge.
	//
	// **Déclarés et non libres**, pour la même raison que les champs de
	// configuration : c'est ce qui permet à l'éditeur de proposer les appariements
	// possibles et aux gabarits d'être validés à l'enregistrement, plutôt que de
	// découvrir à l'exécution qu'on interpole une clé qui n'existe pas.
	Payload []Field `json:"payload"`
}

// ActionField déclare un paramètre attendu par une action de plugin.
//
// Distinct de [Field], et pas par étourderie : un réglage de plugin se saisit une
// fois pour un projet et vaut pour tout ce qu'il fait, tandis qu'un paramètre
// d'action se saisit **par geste** et admet des gabarits liés à la charge du fait.
// Les fondre donnerait un type dont la moitié des champs n'aurait pas de sens
// selon l'endroit où on le lit.
type ActionField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Required : la valeur doit être là, éventuellement après interpolation.
	Required bool `json:"required,omitempty"`
	// Template dit que la valeur admet des variables `{clé}`, liées à la charge
	// du fait déclencheur.
	Template bool `json:"template,omitempty"`
	// OneOf ferme le jeu des valeurs acceptées.
	//
	// Déclaré par le plugin plutôt que codé dans la validation : l'état d'un
	// statut de commit est un vocabulaire de **forge**, et l'écrire ailleurs
	// ferait qu'une seconde forge aux états différents demanderait de modifier
	// le validateur pour ajouter un plugin.
	OneOf       []string `json:"one_of,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// ActionSpec déclare un geste qu'un plugin sait mettre en œuvre.
//
// Le pendant de [EventSpec], et la moitié qui manquait : un plugin qui constate
// sans pouvoir agir laisse toute **conséquence** au noyau, et brancher une
// nouvelle façon d'agir redemande alors de modifier celui-ci — exactement ce que
// la déclaration des faits avait corrigé dans l'autre sens (doc 14 §14.2).
//
// # Aucun champ sensible, et c'est structurel
//
// Une action interne **désigne** un jeton, parce qu'elle parle à un destinataire
// quelconque. Une action de plugin parle à **sa** frontière, dont la connexion est
// déjà réglée et scellée : elle n'a rien à demander. La conséquence vaut d'être
// dite — une action de plugin ne peut pas servir à faire sortir un secret vers une
// cible choisie dans un formulaire, puisqu'elle ne choisit pas sa cible.
type ActionSpec struct {
	// Suffix est la référence **sans** son préfixe : `status`, `comment`.
	//
	// Préfixée par [Descriptor.ActionRef], comme les faits le sont par
	// [Descriptor.EventType] : deux forges déclarant `status` produiraient sinon
	// des gestes indiscernables.
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
	// Summary dit ce que l'action fait, en une phrase, dans l'éditeur.
	Summary string        `json:"summary"`
	Fields  []ActionField `json:"fields"`
}

// Descriptor déclare un plugin et ses réglages.
type Descriptor struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Label string `json:"label"`
	// Summary dit ce que le plugin fait, en une phrase, sur l'écran
	// d'administration.
	Summary string `json:"summary"`
	// Global énumère les réglages de l'instance, qui servent de défaut.
	Global []Field `json:"global"`
	// Project énumère les réglages qu'un projet peut poser ou surcharger.
	//
	// Vide n'empêche pas [Descriptor.PerProject] : un plugin peut se choisir par
	// projet sans rien avoir à régler — le bon de livraison est dans ce cas, on
	// veut seulement dire s'il s'applique.
	Project []Field `json:"project"`
	// Events énumère les faits que ce plugin sait observer.
	//
	// Vide pour un plugin qui n'observe rien : le bon de livraison produit des
	// livrables, il ne constate pas.
	Events []EventSpec `json:"events,omitempty"`
	// Actions énumère les gestes que ce plugin sait mettre en œuvre.
	//
	// Vide pour un plugin qui ne fait que constater. Observer et agir sont deux
	// capacités séparées : un attesteur dit ce qui tourne sans rien pouvoir
	// changer, et lui prêter des actions par symétrie afficherait des gestes que
	// rien n'exécute.
	Actions []ActionSpec `json:"actions,omitempty"`
	// PerProject dit qu'un projet décide de son activation.
	//
	// Faux pour ce qui vaut nécessairement pour toute l'instance : le namespace de
	// Flux n'est pas une propriété d'un projet, et prétendre le contraire
	// afficherait un interrupteur qui ne fait rien.
	PerProject bool `json:"per_project"`
}

// Registry énumère les plugins que ce binaire embarque.
//
// Une fonction et non une variable : rendre une tranche partagée laisserait un
// appelant la trier ou y écrire, et le registre suivant en hériterait.
func Registry() []Descriptor {
	descriptors := []Descriptor{
		{
			ID: "redmine", Kind: KindTracker, Label: "Redmine",
			Summary: "Versions, demandes et avancement lus dans une instance Redmine.",
			// Un seul fait propre au plugin, et c'est un retrait assumé.
			//
			// La sonde en constate bien deux, mais la version est passée au
			// **noyau** — `version.created`, sans préfixe — parce que tous les
			// suivis déposent leurs versions dans la même table, et qu'une réaction
			// déclarée dessus n'a pas à être réécrite le jour où le client change
			// d'outil (doc 15 §15.3). La déclarer ici en plus la ferait apparaître
			// deux fois dans l'éditeur, dont une qui ne se déclencherait jamais.
			//
			// La demande close reste ici : elle n'a pas d'équivalent au noyau. Et
			// c'est un **événement** — quelque chose est arrivé une fois. Une
			// demande qui change de statut n'en est pas un : elle en change dix
			// fois, et un fait par changement ferait du fil des faits un journal de
			// saisie.
			Events: []EventSpec{
				{
					Suffix: "issue.closed",
					Label:  "Demande close",
					Payload: []Field{
						{Name: "issue_id", Label: "Numéro"},
						{Name: "subject", Label: "Sujet"},
						{Name: "tracker", Label: "Type"},
						{Name: "status", Label: "Statut"},
						{Name: "version_id", Label: "Version visée"},
					},
				},
			},
			// La connexion est déclarée aux deux portées. Globalement, elle sert
			// de défaut au déploiement mono-client — celui qui existe. Au niveau
			// d'un projet, elle permet à un second client d'avoir son serveur sans
			// que le premier ait rien à saisir.
			Global: []Field{
				{
					Name: "url", Label: "URL de l'instance", Type: FieldURL,
					Help:        "Racine de Redmine, sans chemin.",
					Placeholder: "https://redmine.example.fr",
				},
				{
					Name: "api_key", Label: "Clé d'API", Type: FieldText, Secret: true,
					Help: "Lecture seule suffit : le dashboard consulte, il n'écrit " +
						"jamais dans le suivi. La clé est scellée en base et n'est " +
						"jamais réaffichée.",
				},
			},
			Project: []Field{
				{
					Name: "project", Label: "Identifiant du projet", Type: FieldText,
					Required: true,
					Help: "Tel qu'il apparaît dans l'URL Redmine, et non son " +
						"libellé.",
					Placeholder: "mon-projet",
				},
				{
					Name: "review_statuses", Label: "Statuts de revue", Type: FieldList,
					Help: "Statuts qui rendent une demande éligible à la note de " +
						"release. Le vocabulaire appartient au client.",
					Placeholder: "En revue",
				},
				{
					Name: "url", Label: "URL de l'instance", Type: FieldURL,
					Help:        "Vide : celle de l'instance s'applique.",
					Placeholder: "https://redmine.example.fr",
				},
				{
					Name: "api_key", Label: "Clé d'API", Type: FieldText, Secret: true,
					Help: "Vide : celle de l'instance s'applique.",
				},
			},
			PerProject: true,
		},
		{
			ID: "gitea", Kind: KindForge, Label: "Gitea",
			Summary: "Branches, tags et références proposés au formulaire de release, " +
				"détection des demandes de fusion ouvertes, et retour vers la forge.",
			Actions: []ActionSpec{
				{
					Suffix: "status", Label: "Statut de commit",
					Summary: "Pose un statut sur un commit, visible dans la forge à côté " +
						"de la demande de fusion.",
					Fields: []ActionField{
						{
							Name: "commit", Label: "Commit", Required: true, Template: true,
							Help: "Le SHA visé. `{git_commit}` pour celui de la " +
								"construction qui vient de conclure.",
							Placeholder: "{git_commit}",
						},
						{
							Name: "state", Label: "État", Required: true,
							OneOf: []string{"pending", "success", "failure", "error"},
							Help: "Le vocabulaire de la forge, et il est fermé. Ce " +
								"n'est **pas** celui d'un fait : `{outcome}` rend " +
								"`succeeded`, que la forge accepte sans savoir " +
								"l'afficher. Pour refléter l'issue, écrire l'état en " +
								"dur et apparier la réaction sur `outcome`.",
							Placeholder: "success",
						},
						{
							Name: "context", Label: "Contexte", Required: true,
							Help: "Le nom sous lequel le statut apparaît. Deux gestes " +
								"partageant ce nom se remplacent : c'est ainsi qu'un " +
								"`pending` devient un `success`.",
							Placeholder: "mse-dashboard/vérifications",
						},
						{
							Name: "description", Label: "Description", Template: true,
							Help: "Une ligne, avec des variables `{tag}`, `{outcome}`.",
						},
						{
							Name: "target_url", Label: "Lien", Template: true,
							Help: "Où mène le statut depuis la forge. Vide, il ne mène " +
								"nulle part et oblige à revenir ici à la main.",
						},
					},
				},
				{
					Suffix: "comment", Label: "Commentaire sur une demande de fusion",
					Summary: "Ajoute un commentaire à une demande de fusion. Le numéro " +
						"vient du fait qui l'a fait connaître.",
					Fields: []ActionField{
						{
							Name: "number", Label: "Numéro", Required: true, Template: true,
							Help: "`{number}`, tel que le fait « demande de fusion " +
								"ouverte » le porte.",
							Placeholder: "{number}",
						},
						{
							Name: "body", Label: "Message", Required: true, Template: true,
							Help: "Markdown, avec les variables de la charge du fait.",
						},
					},
				},
			},
			Events: []EventSpec{{
				Suffix: "pull_request.opened",
				Label:  "Demande de fusion ouverte",
				Payload: []Field{
					{Name: "number", Label: "Numéro"},
					{Name: "title", Label: "Titre"},
					{Name: "author", Label: "Auteur"},
					{Name: "source_branch", Label: "Branche source"},
					{Name: "target_branch", Label: "Branche cible"},
					{Name: "url", Label: "Lien", Type: FieldURL},
				},
			}},
			Global: []Field{
				{
					Name: "url", Label: "URL de la forge", Type: FieldURL,
					Placeholder: "https://forge.example.fr",
				},
				{
					Name: "token", Label: "Jeton", Type: FieldText, Secret: true,
					Help: "Une portée `read:repository` suffit pour lire et observer, " +
						"et vaut mieux : un jeton complet ouvre la forge entière au " +
						"nom de son détenteur. `write:repository` n'est nécessaire " +
						"que pour poser des tags, des statuts ou des commentaires.",
				},
			},
			// Pas de **réglage** par projet : le dépôt est déjà déclaré dans ses
			// paramètres généraux, et le redemander ici ferait deux endroits où le
			// corriger — donc un endroit où oublier.
			//
			// Mais bien une **activation** par projet, depuis que ce plugin observe :
			// surveiller les demandes de fusion d'un dépôt est une décision de
			// projet, même quand la connexion vaut pour toute l'instance. Un projet
			// qui n'en veut pas doit pouvoir le dire sans priver les autres.
			PerProject: true,
		},
		{
			ID: ModuleMSE, Kind: KindModule, Label: "MSE",
			Summary: "Environnements, promotion par Flux et attestation par PortalData.",
			Global: []Field{
				{
					Name: "namespace", Label: "Namespace Flux", Type: FieldText,
					Help:        "Là où vivent les objets Flux, distinct de celui des pipelines.",
					Placeholder: "flux-system",
				},
			},
			// Pour toute l'instance : les environnements ne sont pas une propriété
			// d'un projet. Éteint, l'instance n'a ni onglet Environnements, ni
			// promotion, ni observation (doc 16 §16.8).
			PerProject: false,
		},
		{
			ID: "bon-de-livraison", Kind: KindExporter, Label: "Bon de livraison",
			Summary: "Livrable contractuel récapitulant le périmètre et les vérifications.",
			// Par projet, et c'est la correction d'un défaut : le bon était proposé
			// sur toute release, y compris celles de projets qui n'en produisent
			// aucun.
			PerProject: true,
		},
	}

	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].ID < descriptors[j].ID
	})
	return descriptors
}

// ErrUnknown signale un identifiant qu'aucun plugin ne porte.
var ErrUnknown = errors.New("plugin inconnu")

// Get rend le descripteur d'un plugin.
//
// L'erreur nomme les plugins connus : chercher lequel existe dans le code source
// n'est pas une réponse à donner à quelqu'un qui lit un message d'API.
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
	return Descriptor{}, fmt.Errorf("%w %q (embarqués: %s)",
		ErrUnknown, id, strings.Join(known, ", "))
}

// EventType compose le type complet d'un fait de ce plugin.
//
// `gitea` + `pull_request.opened` donne `gitea.pull_request.opened`. Deux plugins
// de forge coexisteront, et une réaction doit dire lequel elle écoute : un type nu
// deviendrait ambigu au premier ajout, et l'ambiguïté se manifesterait par un plan
// qui part sur le fait d'un autre.
func (d Descriptor) EventType(spec EventSpec) string {
	return d.ID + "." + spec.Suffix
}

// Observes dit si le plugin déclare au moins un fait.
func (d Descriptor) Observes() bool { return len(d.Events) > 0 }

// EventTypes énumère les types complets déclarés par tous les plugins embarqués.
//
// Sert à l'éditeur, qui propose les faits appariables, et à la validation d'une
// réaction : un type qu'aucun plugin ne déclare est refusé à l'enregistrement
// plutôt que de rester silencieux pour toujours.
func EventTypes() []string {
	var types []string
	for _, descriptor := range Registry() {
		for _, spec := range descriptor.Events {
			types = append(types, descriptor.EventType(spec))
		}
	}
	sort.Strings(types)
	return types
}

// ActionRef rend la référence complète d'une action, préfixée par le plugin.
func (d Descriptor) ActionRef(spec ActionSpec) string {
	return d.ID + "." + spec.Suffix
}

// Acts dit si le plugin déclare au moins une action.
func (d Descriptor) Acts() bool { return len(d.Actions) > 0 }

// FindAction retrouve le plugin qui déclare une référence d'action.
//
// La connaissance du préfixe vit ici, avec celle qui le pose : chercher un point
// dans la référence et prendre ce qui précède est un découpage qu'un appelant
// referait à sa façon, et `forge.status` — une action interne d'une version
// antérieure — ressemble alors à une action du plugin `forge`.
//
// Rendue sans consulter l'activation du plugin : un plan **figé** dans une
// exécution doit rester lisible même après extinction, et c'est à l'appelant de
// décider si ce qu'il fait de la réponse demande un plugin allumé.
func FindAction(ref string) (Descriptor, ActionSpec, bool) {
	for _, descriptor := range Registry() {
		for _, spec := range descriptor.Actions {
			if descriptor.ActionRef(spec) == ref {
				return descriptor, spec, true
			}
		}
	}
	return Descriptor{}, ActionSpec{}, false
}

// Fields rend les champs d'une portée.
func (d Descriptor) Fields(scope Scope) []Field {
	if scope == ScopeProject {
		return d.Project
	}
	return d.Global
}

// Scope est la portée d'un jeu de réglages.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Values porte les réglages d'un plugin, champ par champ.
//
// Des chaînes, y compris pour les booléens et les listes. Le format suit celui
// des variables d'environnement qu'il remplace : une configuration migrée se
// recopie sans traduction, et un champ ajouté ne demande pas de migration de
// schéma.
type Values map[string]string

// Validate refuse un jeu de réglages incohérent avec le descripteur.
//
// `resolved` porte les valeurs déjà connues à la portée supérieure : c'est contre
// **elles** que l'obligation est vérifiée, et non contre la seule saisie. Un
// projet qui laisse l'URL vide parce que l'instance la fournit ne doit pas être
// refusé ; un projet qui la laisse vide alors que personne ne la fournit doit
// l'être — et l'écart entre ces deux cas ne se voit qu'ici.
func (d Descriptor) Validate(scope Scope, values Values, resolved Values) error {
	fields := d.Fields(scope)
	declared := make(map[string]Field, len(fields))
	for _, field := range fields {
		declared[field.Name] = field
	}

	var problems []error

	// Les champs inconnus sont refusés plutôt qu'ignorés. Un champ ignoré donne
	// un écran qui dit « enregistré » sur une valeur que rien ne relira, et c'est
	// une faute de frappe qui se découvre des semaines plus tard.
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, known := declared[name]; !known {
			problems = append(problems, fmt.Errorf(
				"le plugin %q ne déclare aucun réglage %q à la portée %q",
				d.ID, name, scope))
		}
	}

	for _, field := range fields {
		value := strings.TrimSpace(values[field.Name])

		if field.Required {
			effective := value
			if effective == "" {
				effective = strings.TrimSpace(resolved[field.Name])
			}
			if effective == "" {
				problems = append(problems, fmt.Errorf(
					"%s est requis", field.Label))
				continue
			}
		}
		if value == "" {
			continue
		}

		switch field.Type {
		case FieldURL:
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				problems = append(problems, fmt.Errorf(
					"%s doit être une URL absolue (schéma et hôte), reçu %q",
					field.Label, value))
			}
		case FieldBool:
			if value != "true" && value != "false" {
				problems = append(problems, fmt.Errorf(
					"%s vaut « true » ou « false », reçu %q", field.Label, value))
			}
		case FieldText, FieldList:
			// Rien à vérifier : une liste vide de valeurs utiles reste une saisie
			// valide, et borner la forme d'un identifiant de projet imposerait une
			// convention qui appartient au client.
		default:
			problems = append(problems, fmt.Errorf(
				"%s: type de champ %q inconnu", field.Label, field.Type))
		}
	}

	return errors.Join(problems...)
}

// Resolve superpose les réglages d'un projet à ceux de l'instance.
//
// Une valeur vide au niveau du projet **n'efface pas** celle de l'instance : elle
// veut dire « je n'ai rien de particulier à dire ». Effacer demanderait un geste
// distinct, et le confondre avec un champ non rempli ferait perdre une URL au
// premier enregistrement d'un formulaire qui ne l'affichait pas.
func Resolve(global, project Values) Values {
	resolved := make(Values, len(global)+len(project))
	maps.Copy(resolved, global)
	for name, value := range project {
		if strings.TrimSpace(value) == "" {
			continue
		}
		resolved[name] = value
	}
	return resolved
}

// SecretFields nomme les champs d'une portée qui doivent être scellés.
func (d Descriptor) SecretFields(scope Scope) []string {
	var secrets []string
	for _, field := range d.Fields(scope) {
		if field.Secret {
			secrets = append(secrets, field.Name)
		}
	}
	return secrets
}

// IsSecret dit si un champ d'une portée porte une valeur sensible.
func (d Descriptor) IsSecret(scope Scope, name string) bool {
	for _, field := range d.Fields(scope) {
		if field.Name == name {
			return field.Secret
		}
	}
	return false
}

// List découpe la valeur d'un champ [FieldList].
//
// Rendue ici plutôt que chez chaque appelant : deux découpages divergents — l'un
// qui coupe les espaces, l'autre non — donneraient deux lectures de la même
// configuration, et l'écart ne se verrait que sur un statut nommé « En revue ».
func List(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}
