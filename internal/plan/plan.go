// Package plan porte le graphe d'exécution d'une réaction, et son avancement.
//
// # Sans entrées-sorties, délibérément
//
// Rien ici ne touche la base ni le cluster. L'avancement d'un plan — quelle étape
// est réglée, quel geste part ensuite, que faire d'un échec — est de la **logique
// de décision**, et c'est le même geste que pour `internal/release` : elle
// s'éprouve sans monter quoi que ce soit.
//
// Ce n'est pas une préférence de style. Un moteur d'enchaînement ne se voit
// tourner en entier que rarement, et sur des exécutions qui durent des dizaines
// de minutes ; la seule façon d'avoir confiance dans ses cas tordus — une étape
// vide, un geste qui ne peut pas partir, un échec au milieu — est de pouvoir les
// écrire en trois lignes.
//
// # Des étapes, pas un graphe
//
// Un plan est une **suite d'étapes** ; chaque étape porte des **gestes** exécutés
// en parallèle ; l'étape suivante démarre quand la précédente est réglée.
//
// Le graphe libre a été écarté (doc 14 §14.2). Il appelle un langage
// d'expressions, un ordonnanceur de graphe, une détection de cycles et une
// interface que personne ne sait lire — pour une expressivité que le besoin
// exprimé ne réclame pas : « poser un statut, puis trois constructions en
// parallèle, puis quatre tests si elles ont réussi » est une suite de quatre
// étapes.
//
// La forme retenue a une seconde vertu, moins évidente : elle **se dessine**. Des
// étapes empilées et des gestes en colonnes se lisent d'un coup d'œil, là où un
// DAG aurait demandé un éditeur de graphe.
package plan

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// GesteKind dit ce qu'un geste met en œuvre.
//
// Jeu **fermé** : ce sont les deux choses que le moteur sait faire. Un geste de
// type pipeline **est** un job de la file existante, avec ses plafonds, ses
// classes d'exclusion et son plafond de tentatives — il n'y a pas de second
// exécuteur, et c'est ce qui fait tenir l'édifice.
type GesteKind string

const (
	// GestePipeline enfile une famille du catalogue.
	GestePipeline GesteKind = "pipeline"
	// GesteAction exécute une action interne ou fournie par un plugin.
	GesteAction GesteKind = "action"
)

// When borne le déclenchement d'un geste à l'issue de l'étape précédente.
//
// Trois valeurs, et pas un langage d'expressions. Le jour où quelqu'un demandera
// « si le score dépasse 80 », la bonne réponse sera d'ajouter un opérateur nommé,
// pas un interpréteur : une configuration qui devient un programme est une
// configuration qu'on ne peut plus relire, et le dashboard n'offre aucun
// débogueur pour elle.
type When string

const (
	// WhenSuccess ne part que si l'étape précédente n'a rien fait échouer. Vaut
	// par défaut : c'est l'enchaînement qu'on écrit sans y penser.
	WhenSuccess When = "success"
	// WhenFailure ne part que si l'étape précédente a échoué. Sert à réagir à un
	// échec — poser un statut rouge, prévenir.
	WhenFailure When = "failure"
	// WhenAlways part dans les deux cas. Sert à conclure : un statut final, un
	// nettoyage.
	WhenAlways When = "always"
)

// Geste est une chose à faire.
type Geste struct {
	Kind GesteKind `json:"kind"`
	// Ref nomme la famille du catalogue pour un pipeline, l'action pour une
	// action. Ce que la référence désigne est vérifié **à l'enregistrement**
	// contre le catalogue et le registre — ce paquet ne les connaît pas.
	Ref string `json:"ref"`
	// Label nomme le geste à l'écran. Vide, la référence fait l'affaire.
	Label string `json:"label,omitempty"`
	// When vide vaut [WhenSuccess].
	When When `json:"when,omitempty"`
	// Params est ce que la déclaration fixe. Ce qui dépend du fait déclencheur —
	// la révision construite, le tag — est lié par le moteur au moment de partir :
	// un plan qui figerait ces valeurs ne servirait qu'une fois.
	Params map[string]string `json:"params,omitempty"`
}

// Condition rend la règle effective, l'absence valant [WhenSuccess].
func (g Geste) Condition() When {
	if g.When == "" {
		return WhenSuccess
	}
	return g.When
}

// Name rend de quoi nommer le geste à l'écran.
func (g Geste) Name() string {
	if strings.TrimSpace(g.Label) != "" {
		return g.Label
	}
	return g.Ref
}

// Stage est un jeu de gestes exécutés en parallèle.
// Role dit ce qu'une étape **est**, pour ce qui en dépend au-dehors.
//
// # Pourquoi l'étape porte cela
//
// La vie d'une release avance à deux moments : quand elle est construite, et
// quand elle est vérifiée. Il faut donc un point d'ancrage nommé dans le plan.
//
// « La conclusion du plan » n'en est pas un : elle confond *l'automatisation est
// terminée* avec *la release est vérifiée*. Un plan qui finirait par une
// notification ferait alors échouer la release sur un webhook injoignable.
//
// Un geste composite — un geste `build` contenant des pipelines — n'en est pas un
// non plus, et pour une raison de modèle : l'étape **est déjà** le groupe. Elle
// rassemble des gestes parallèles et le moteur calcule déjà sa conclusion. Un
// niveau d'imbrication de plus referait ce qu'elle fait (doc 15 §15.3).
//
// # Une étape sans rôle ne touche à rien
//
// C'est le défaut par lequel on entre, et c'est ce qui rend une étape de
// notification inoffensive pour le cycle de vie d'une release.
type Role string

const (
	// RoleBuild produit les artefacts d'une release.
	RoleBuild Role = "build"
	// RoleTest rend un verdict sur ces artefacts.
	RoleTest Role = "test"
)

// Roles énumère les rôles connus, pour l'éditeur et la validation.
func Roles() []Role { return []Role{RoleBuild, RoleTest} }

type Stage struct {
	// Label nomme l'étape à l'écran. Facultatif.
	Label string `json:"label,omitempty"`
	// Role rattache l'étape au cycle de vie d'une release. Vide : l'étape
	// s'exécute sans rien y changer.
	//
	// Ce paquet vérifie que la valeur est connue. Que les gestes de l'étape
	// produisent bien ce que le rôle annonce se vérifie **à l'enregistrement**,
	// contre le catalogue — que ce paquet ne connaît pas, délibérément.
	Role   Role    `json:"role,omitempty"`
	Gestes []Geste `json:"gestes"`
}

// Plan est une suite d'étapes.
type Plan struct {
	Stages []Stage `json:"stages"`
}

// Empty dit qu'un plan n'a rien à faire.
//
// Distingué d'un plan invalide : « ce projet n'enchaîne rien » est une
// configuration parfaitement légitime, et l'appelant doit pouvoir conclure tout
// de suite plutôt qu'attendre un geste qui ne viendra pas.
func (p Plan) Empty() bool {
	for _, stage := range p.Stages {
		if len(stage.Gestes) > 0 {
			return false
		}
	}
	return true
}

// ErrInvalid signale un plan qu'on refuse d'exécuter.
var ErrInvalid = errors.New("plan invalide")

// Validate refuse un plan qui ne pourrait pas s'exécuter comme il se lit.
//
// Vérifié à l'enregistrement et non au premier déclenchement : un plan fautif
// découvert au moment où il devait partir laisse une release en attente d'un
// geste que rien n'exécutera, et le diagnostic se fait alors sur une chaîne
// arrêtée plutôt que devant un formulaire.
func (p Plan) Validate() error {
	if len(p.Stages) == 0 {
		return fmt.Errorf("%w: aucune étape", ErrInvalid)
	}

	var problems []error
	for index, stage := range p.Stages {
		if len(stage.Gestes) == 0 {
			problems = append(problems, fmt.Errorf(
				"%w: l'étape %d ne porte aucun geste", ErrInvalid, index+1))
			continue
		}
		problems = append(problems, stage.validateRole(index)...)
		for rank, geste := range stage.Gestes {
			problems = append(problems, geste.validate(index, rank)...)
		}
	}
	return errors.Join(problems...)
}

// validateRole refuse un rôle que ce paquet ne connaît pas.
func (s Stage) validateRole(index int) []error {
	if s.Role == "" || slices.Contains(Roles(), s.Role) {
		return nil
	}

	known := make([]string, 0, len(Roles()))
	for _, role := range Roles() {
		known = append(known, string(role))
	}
	// Un rôle inventé serait ignoré par le cycle de vie, et l'étape s'exécuterait
	// sans jamais faire avancer la release. Le refuser à l'enregistrement évite
	// une release qui reste en construction sans que rien ne le dise.
	return []error{fmt.Errorf("%w: l'étape %d porte le rôle %q (connus: %s)",
		ErrInvalid, index+1, s.Role, strings.Join(known, ", "))}
}

// LastWithRole rend l'indice de la dernière étape portant ce rôle, ou -1.
//
// La **dernière**, et non la première : un plan peut construire en deux temps —
// une image de base, puis l'application au-dessus — et c'est la conclusion du
// dernier de ces temps qui dit que la construction est faite.
func (p Plan) LastWithRole(role Role) int {
	found := -1
	for index, stage := range p.Stages {
		if stage.Role == role {
			found = index
		}
	}
	return found
}

func (g Geste) validate(stage, rank int) []error {
	var problems []error
	where := fmt.Sprintf("étape %d, geste %d", stage+1, rank+1)

	switch g.Kind {
	case GestePipeline, GesteAction:
	default:
		problems = append(problems, fmt.Errorf(
			"%w: %s: type %q inconnu (attendu %s ou %s)",
			ErrInvalid, where, g.Kind, GestePipeline, GesteAction))
	}

	if strings.TrimSpace(g.Ref) == "" {
		problems = append(problems, fmt.Errorf(
			"%w: %s: référence absente — un geste doit dire ce qu'il met en œuvre",
			ErrInvalid, where))
	}

	switch g.Condition() {
	case WhenSuccess, WhenFailure, WhenAlways:
	default:
		problems = append(problems, fmt.Errorf(
			"%w: %s: condition %q inconnue (attendu %s, %s ou %s)",
			ErrInvalid, where, g.When, WhenSuccess, WhenFailure, WhenAlways))
	}

	// Un geste de la **première** étape conditionné à un échec ne partira jamais :
	// rien ne la précède, et l'issue conventionnelle d'une étape absente est la
	// réussite. Refusé plutôt que laissé en place, parce qu'une déclaration qui ne
	// peut pas se déclencher se lit comme une déclaration qui ne se déclenche pas
	// — et on cherche alors la panne ailleurs.
	if stage == 0 && g.Condition() == WhenFailure {
		problems = append(problems, fmt.Errorf(
			"%w: %s: condition %q sur la première étape — rien ne la précède, "+
				"ce geste ne partirait jamais", ErrInvalid, where, WhenFailure))
	}

	return problems
}

/*
 * L'avancement.
 *
 * Une fonction pure d'un plan et de l'état de ses gestes vers ce qu'il reste à
 * faire. Le moteur persiste les états et exécute ; il ne décide de rien.
 */

// State est le sort d'un geste.
type State string

const (
	Pending   State = "pending"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	// Skipped : la condition du geste n'était pas remplie. Distinct d'un échec —
	// un geste sauté n'a rien raté — et distinct d'un `pending`, qui attendrait
	// encore.
	Skipped State = "skipped"
)

// Terminal dit qu'un geste ne bougera plus.
func (s State) Terminal() bool {
	return s == Succeeded || s == Failed || s == Skipped
}

// Position désigne un geste dans un plan.
type Position struct {
	Stage int `json:"stage"`
	Geste int `json:"geste"`
}

// Progress est un plan et l'état de ses gestes.
type Progress struct {
	Plan Plan
	// States suit la forme du plan : une tranche par étape, un état par geste.
	States [][]State
}

// Start rend l'avancement initial d'un plan : tout en attente.
func Start(p Plan) Progress {
	states := make([][]State, len(p.Stages))
	for index, stage := range p.Stages {
		states[index] = make([]State, len(stage.Gestes))
		for rank := range stage.Gestes {
			states[index][rank] = Pending
		}
	}
	return Progress{Plan: p, States: states}
}

// Step est ce que le moteur doit faire d'un geste en attente.
type Step struct {
	Position Position
	Geste    Geste
	// Run dit s'il doit partir. Faux : sa condition n'est pas remplie, et il est
	// à marquer [Skipped].
	//
	// Une seule réponse pour les deux cas, plutôt que deux fonctions à appeler
	// dans le bon ordre : un moteur qui oublierait la seconde laisserait des
	// gestes en attente pour toujours, et l'étape ne se réglerait jamais.
	Run bool
}

// Next rend les gestes de l'étape courante qui attendent une décision.
//
// Rend une tranche vide quand le plan est réglé, ou quand l'étape courante n'a
// que des gestes déjà partis. Rappelable sans effet : c'est ce qui rend le moteur
// tolérant à une redélivrance, et ce qui permet de le relancer après un
// redémarrage sans tenir d'état en mémoire.
func (p Progress) Next() []Step {
	stage, found := p.current()
	if !found {
		return nil
	}

	// L'issue de l'étape précédente décide des conditions. La première étape n'en
	// a pas : par convention, rien n'a échoué avant elle.
	previousFailed := stage > 0 && p.stageFailed(stage-1)

	var steps []Step
	for rank, geste := range p.Plan.Stages[stage].Gestes {
		if p.States[stage][rank] != Pending {
			continue
		}
		steps = append(steps, Step{
			Position: Position{Stage: stage, Geste: rank},
			Geste:    geste,
			Run:      satisfied(geste.Condition(), previousFailed),
		})
	}
	return steps
}

// satisfied dit si la condition d'un geste est remplie.
func satisfied(condition When, previousFailed bool) bool {
	switch condition {
	case WhenAlways:
		return true
	case WhenFailure:
		return previousFailed
	default:
		return !previousFailed
	}
}

// current rend l'index de la première étape non réglée.
func (p Progress) current() (int, bool) {
	for index := range p.Plan.Stages {
		if !p.stageSettled(index) {
			return index, true
		}
	}
	return 0, false
}

// stageSettled dit qu'une étape ne bougera plus.
func (p Progress) stageSettled(stage int) bool {
	for _, state := range p.States[stage] {
		if !state.Terminal() {
			return false
		}
	}
	return true
}

// stageFailed dit qu'une étape a laissé quelque chose en échec.
//
// Une étape entièrement sautée n'a pas échoué : elle n'a rien tenté. La
// distinction compte pour un geste conditionné à l'échec, qui ne doit pas partir
// parce que l'étape d'avant ne s'est pas exécutée.
func (p Progress) stageFailed(stage int) bool {
	return slices.Contains(p.States[stage], Failed)
}

// Done dit que le plan n'a plus rien à faire.
func (p Progress) Done() bool {
	_, found := p.current()
	return !found
}

// Outcome est l'issue d'un plan réglé.
//
// Échoué dès qu'un geste a échoué, **où qu'il soit**. Un geste de conclusion en
// `always` qui réussit ne rachète pas l'échec qui l'a fait partir : le plan
// rapporterait « réussi » sur une chaîne dont la construction est tombée.
func (p Progress) Outcome() State {
	for stage := range p.Plan.Stages {
		if p.stageFailed(stage) {
			return Failed
		}
	}
	return Succeeded
}

// Mark écrit le sort d'un geste.
//
// Rend une copie : `Progress` est une valeur, et la muter en place ferait qu'un
// appelant qui l'a passée à `Next` verrait sa tranche changer sous lui.
func (p Progress) Mark(position Position, state State) (Progress, error) {
	if position.Stage < 0 || position.Stage >= len(p.States) {
		return p, fmt.Errorf("%w: étape %d hors du plan", ErrInvalid, position.Stage)
	}
	if position.Geste < 0 || position.Geste >= len(p.States[position.Stage]) {
		return p, fmt.Errorf("%w: geste %d hors de l'étape %d",
			ErrInvalid, position.Geste, position.Stage)
	}

	states := make([][]State, len(p.States))
	for index, stage := range p.States {
		states[index] = slices.Clone(stage)
	}
	states[position.Stage][position.Geste] = state
	return Progress{Plan: p.Plan, States: states}, nil
}
