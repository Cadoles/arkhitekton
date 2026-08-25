// Package release porte le cycle de vie d'une release et l'invariant de version.
//
// L'invariant de [B17] tient en une phrase : **une version est une chaîne,
// présente à l'identique en quatre endroits** — le nom de la version Redmine, le
// tag Git, le tag d'image, et le `version.txt` baké dans l'image que `PortalData`
// rapporte ensuite.
//
// Ce n'est pas une commodité de nommage : c'est ce qui rend la jointure possible.
// Si ces valeurs divergent, chaque fonctionnalité du dashboard se dégrade
// différemment — un environnement affiché « divergent » à tort, un écran de
// version vide, une note de release qui compare des intervalles de tags sans
// rapport avec les images promues.
//
// Tout ce paquet est pur : ni base, ni cluster, ni réseau. C'est là que sont les
// décisions, et elles doivent être vérifiables sans rien monter.
package release

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Kind distingue les trois natures de release.
type Kind string

const (
	// KindPre est une pré-release — `1.8.0-rc1`. Destinée aux environnements de
	// test, jusqu'en pré-production incluse.
	KindPre Kind = "pre"
	// KindFinal est la release livrable — `1.8.0`.
	KindFinal Kind = "final"
	// KindPatch corrige une version déjà livrée — `1.8.1`.
	KindPatch Kind = "patch"
)

// State est l'état d'une release.
type State string

const (
	// StateDraft : créée dans le dashboard, tag Git pas encore posé.
	StateDraft State = "draft"
	// StateBuilding : tag posé, `build-image` en cours.
	StateBuilding State = "building"
	// StateVerifying : images disponibles, batterie de tests en cours ou
	// partielle.
	//
	// `verifying` plutôt que `testing` : `testing` est l'ancien nom de
	// l'environnement `qualif` (B13) et reste présent dans le code existant. Deux
	// sens pour un même mot dans le même système est une confusion gratuite.
	StateVerifying State = "verifying"
	// StateReady : tous les tests obligatoires terminés, décision humaine en
	// attente. **Pas encore promouvable** — c'est `released` qui l'autorise.
	StateReady State = "ready"
	// StateReleased : validée, promouvable sur les environnements de rang
	// supérieur.
	StateReleased State = "released"
	// StateFailed : un pipeline obligatoire a échoué. Relançable, ce qui ramène en
	// `verifying` quand les images existent déjà.
	StateFailed State = "failed"
	// StateCancelled : abandonnée. Terminal — reprendre une release abandonnée
	// masquerait la raison de son abandon, alors qu'en créer une nouvelle la
	// laisse lisible.
	StateCancelled State = "cancelled"
)

// transitions décrit les passages autorisés (doc 04 §4.2).
//
// Une table explicite plutôt que des `if` dispersés : le jour où une transition
// manque, elle manque à un seul endroit, et la lire suffit à savoir ce qui est
// possible.
//
// Deux chemins y coexistent, et c'est délibéré :
//
//	construite (pre, patch)
//	  draft ──▶ building ──▶ verifying ──▶ ready ──▶ released
//
//	dérivée (final, re-tag d'une RC validée)
//	  draft ──▶ ready ──▶ released
//
// Le chemin dérivé ne passe ni par `building` ni par `verifying` : rien n'est
// construit, donc rien ne s'exécute (B17). Modéliser un `building` fictif ferait
// attendre l'interface sur un pipeline qui ne viendra jamais.
var transitions = map[State][]State{
	// `draft → ready` est la porte du chemin dérivé ; `draft → building` celle du
	// chemin construit.
	StateDraft:    {StateBuilding, StateReady, StateFailed, StateCancelled},
	StateBuilding: {StateVerifying, StateReady, StateFailed, StateCancelled},
	// `verifying → building` : reconstruire depuis la même référence reste permis,
	// un build pouvant échouer pour une raison extérieure à la release.
	StateVerifying: {StateReady, StateBuilding, StateFailed, StateCancelled},
	// Depuis `ready`, la décision humaine tranche : livrer, ou renvoyer aux tests
	// parce qu'on veut en relancer une famille.
	StateReady: {StateReleased, StateVerifying, StateBuilding, StateFailed, StateCancelled},
	// `released` n'est pas terminal : une release livrée peut être abandonnée —
	// retirée du service après une régression — mais ne redescend pas vers les
	// tests. Rejouer sa validation après coup réécrirait l'histoire d'un livrable
	// déjà parti chez le client.
	StateReleased: {StateCancelled},
	// Un échec est relançable : `verifying` quand les images existent déjà,
	// `building` quand il faut reconstruire.
	//
	// `failed → ready` s'y ajoute, et ce n'est pas un raccourci : une famille de
	// vérification relancée peut réussir, et c'est alors le dernier verdict qui
	// vaut. Sans ce passage, une release restait en échec pour un résultat que
	// l'essai suivant avait remplacé, et le bouton « Rejouer » ne rattrapait
	// rien. La décision de livrer reste humaine — `ready` ne signifie pas livrée.
	StateFailed: {StateBuilding, StateVerifying, StateReady, StateCancelled},
	// `cancelled` est terminal, à dessein : voir plus haut.
	StateCancelled: {},
}

// Terminal dit si un état ne mène nulle part.
func (s State) Terminal() bool { return len(transitions[s]) == 0 }

// CanTransition dit si le passage est autorisé.
func CanTransition(from, to State) bool {
	return slices.Contains(transitions[from], to)
}

// ErrInvalidTransition signale un passage refusé.
type ErrInvalidTransition struct {
	From State
	To   State
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("transition refusée: %s → %s", e.From, e.To)
}

// semverPattern couvre la forme retenue : `MAJOR.MINOR.PATCH` avec pré-release
// facultative. Les métadonnées de build (`+…`) sont volontairement absentes — le
// séparateur retenu pour la promotion est l'underscore, la grammaire des tags OCI
// excluant le `+` (B7).
var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// Version est un tag de release décomposé.
type Version struct {
	Raw string
	// Base est la version sans pré-release : `1.8.0` pour `1.8.0-rc1`. C'est
	// elle qui doit correspondre au nom de la version Redmine.
	Base string
	// Prerelease est vide pour une release finale.
	Prerelease string
}

// IsPrerelease dit si le tag porte une composante de pré-release.
func (v Version) IsPrerelease() bool { return v.Prerelease != "" }

// ParseVersion décompose un tag de release.
//
// Refuser plutôt que tolérer : un tag hors forme casserait la jointure avec
// Redmine et la sélection Flux, et il vaut mieux le dire à la création que le
// découvrir en constatant qu'un environnement ne bouge pas.
func ParseVersion(tag string) (Version, error) {
	matches := semverPattern.FindStringSubmatch(strings.TrimSpace(tag))
	if matches == nil {
		return Version{}, fmt.Errorf(
			"tag %q invalide: forme attendue MAJEUR.MINEUR.CORRECTIF[-prerelease]", tag)
	}

	return Version{
		Raw:        matches[0],
		Base:       fmt.Sprintf("%s.%s.%s", matches[1], matches[2], matches[3]),
		Prerelease: matches[4],
	}, nil
}

// CheckKind vérifie que la nature annoncée correspond à la forme du tag.
//
// L'erreur que cela attrape est banale et coûteuse : créer `1.8.0-rc1` en
// déclarant une release finale, puis constater que la production ne la prend
// jamais — sa plage SemVer excluant les pré-releases, Flux l'ignore sans rien
// dire (B7).
func CheckKind(version Version, kind Kind) error {
	switch kind {
	case KindPre:
		if !version.IsPrerelease() {
			return fmt.Errorf(
				"le tag %q n'a pas de composante de pré-release: une pré-release s'écrit par exemple %s-rc1",
				version.Raw, version.Base)
		}
	case KindFinal, KindPatch:
		if version.IsPrerelease() {
			return fmt.Errorf(
				"le tag %q porte une composante de pré-release %q: une release %s s'écrit %s",
				version.Raw, version.Prerelease, kind, version.Base)
		}
	default:
		return fmt.Errorf("nature de release inconnue %q", kind)
	}
	return nil
}

// CheckRedmineVersion vérifie la jointure avec Redmine.
//
// `tag de release = nom de version Redmine [+ suffixe de pré-release]`. Une RC
// `3.2.0-rc1` se rattache donc à la version Redmine `3.2.0` : c'est ce qui relie
// une pré-release à son périmètre fonctionnel.
//
// La comparaison est normalisée ([Normalizer.Normalize]) et non stricte.
// L'égalité exacte, telle que B8 et B17 la posaient, rejetait toute release
// réelle : les versions de l'instance s'appellent `v3.2.0`, jamais `3.2.0`.
func (n Normalizer) CheckRedmineVersion(version Version, redmineName string) error {
	if !n.SameVersion(version, redmineName) {
		return fmt.Errorf(
			"le tag %q se rattache à la version %q, or la version Redmine visée est %q",
			version.Raw, version.Base, strings.TrimSpace(redmineName))
	}
	return nil
}

// ErrSourceRequired signale une release finale sans RC source.
var ErrSourceRequired = errors.New("une release finale par re-tag exige une pré-release source")

// Source décrit la pré-release réutilisée par une release finale.
type Source struct {
	Tag          string
	ImageVersion string
	State        State
	// TestsGreen vaut false si les résultats de la RC ne sont pas au vert. La
	// release reste possible — c'est une décision humaine — mais elle doit être
	// signalée.
	TestsGreen bool
}

// FinalFromSource compose l'image d'une release finale à partir de sa RC.
//
// Le passage RC → finale se fait par **re-tag** : ce qui part en production est
// bit pour bit ce qui a été validé en pré-production. La contrepartie est que
// l'image continue de porter la version de la RC — son `version.txt` a été baké
// au build — et c'est donc `PortalData` qui rapportera `1.8.0-rc3` là où `1.8.0`
// est déployée.
//
// D'où `image_version`, distinct de `tag` : comparer `PortalData` au tag
// échouerait sur **toute** release finale (B8, B17).
func FinalFromSource(source Source) (imageVersion string, warnings []string, err error) {
	if strings.TrimSpace(source.Tag) == "" {
		return "", nil, ErrSourceRequired
	}
	// La RC source doit être **validée**, et non seulement prête (doc 04 §4.2).
	// La distinction porte tout le sens du re-tag : il transporte la validation de
	// la RC vers la release finale, donc il n'y a rien à transporter tant que
	// personne n'a validé. Accepter une RC `ready` reviendrait à livrer sur la foi
	// de tests verts, alors que le cycle réserve cette décision à un humain.
	if source.State != StateReleased {
		return "", nil, fmt.Errorf(
			"la pré-release %q est dans l'état %q: seule une release %q peut être re-taguée",
			source.Tag, source.State, StateReleased)
	}

	imageVersion = source.ImageVersion
	if imageVersion == "" {
		// Une RC dont l'image n'est pas identifiée ne peut pas être re-taguée :
		// il n'y a rien à promouvoir.
		return "", nil, fmt.Errorf("la pré-release %q ne porte aucune version d'image", source.Tag)
	}

	if !source.TestsGreen {
		// Avertissement et non refus : livrer une RC dont un test est rouge est
		// une décision qui appartient au client. La taire serait en revanche une
		// faute.
		warnings = append(warnings, fmt.Sprintf(
			"les résultats de la pré-release %q ne sont pas au vert", source.Tag))
	}

	return imageVersion, warnings, nil
}
