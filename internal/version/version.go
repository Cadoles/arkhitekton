// Package version porte le schéma de version, à qui le reste **pose des
// questions** au lieu de lire des champs.
//
// Le SemVer n'était pas une brique de ce projet, c'était une **hypothèse
// ambiante** : la forme `MAJEUR.MINEUR.CORRECTIF[-prerelease]` était codée dans
// `internal/release`, et tout ce qui s'y appuie — la sélection par plage, la
// déduction de la RC suivante, la jointure avec le suivi — la supposait acquise
// sans jamais la nommer (doc 12 §12.4).
//
// Elle n'est pourtant pas universelle. Le voisinage immédiat en donne
// l'exemple : les tags de `mse-kustom` s'écrivent
// `2026.7.27-k8sunstable.1651.91a39c48b`, du calendaire déguisé. D'autres projets
// numérotent leurs constructions, ou n'ordonnent rien du tout.
//
// # Ce que le schéma répond, et ce qu'il peut ignorer
//
// Six questions, et **chacune peut rester sans réponse**. C'est le point de
// conception : un schéma opaque doit rester *utilisable*. Un projet dont les tags
// s'écrivent `build-4711` perd la suggestion de version suivante et la sélection
// par plage, mais garde tout le reste — construire, tester, noter, déployer par
// tag explicite. L'outil doit le **dire** plutôt que calculer n'importe quoi.
//
//	Question                         SemVer        Opaque
//	------------------------------   -----------   --------------
//	Ce tag est-il bien formé ?       oui           tout l'est
//	Quelle est sa ligne de version ? `3.2.0`       inconnue
//	Est-ce une pré-release ?         oui/non       inconnue
//	Comment y attacher un marqueur ? métadonnée    impossible
//	Comment ordonner deux versions ? précédence    inconnu
//
// # Pourquoi une capacité et non un fournisseur
//
// Ce n'est pas une source extérieure : rien n'est lu ailleurs, aucune panne n'est
// possible. C'est une **convention du projet**, déclarée dans sa configuration,
// et c'est pourquoi ce paquet vit à part de `internal/provider`.
package version

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnsupported dit qu'un schéma ne sait pas répondre à cette question.
//
// Pas une panne, et c'est toute la nuance : le schéma opaque rend cette erreur
// pour la moitié des questions, et un projet qui l'emploie fonctionne. L'appelant
// doit **dire** qu'il ne sait pas, jamais deviner — une version suivante inventée
// ou un ordre arbitraire produiraient des décisions fausses que rien ne
// signalerait.
var ErrUnsupported = errors.New("le schéma de version ne répond pas à cette question")

// Version est un tag décomposé autant que le schéma en est capable.
type Version struct {
	// Raw est le tag tel qu'il a été écrit. Toujours renseigné : c'est la seule
	// chose que tout schéma connaît.
	Raw string
	// Base est la ligne de version — `1.8.0` pour `1.8.0-rc1`. Vide quand le
	// schéma ne la connaît pas.
	//
	// Nommée `Base` et non `Line` comme le fait le document : c'est le mot déjà
	// employé partout dans le code et dans les messages que l'utilisateur lit,
	// et le changer n'aurait rien apporté qu'un risque.
	Base string
	// Prerelease est la composante de pré-release. Vide quand il n'y en a pas —
	// ou quand le schéma ne sait pas la distinguer, d'où [Version.Prereleased].
	Prerelease string
	// KnowsPrerelease dit si la question « est-ce une pré-release » a un sens
	// pour ce schéma. Faux, `Prerelease` vide ne signifie **pas** « release
	// finale » : il signifie « on ne sait pas ».
	KnowsPrerelease bool
}

// Prereleased dit si le tag est une pré-release, et si la question a un sens.
//
// Deux retours plutôt qu'un booléen : sur un schéma opaque, rendre `false` seul
// affirmerait qu'une release est finale alors que rien ne le dit. C'est
// exactement le genre d'affirmation qui fait promouvoir en production un tag que
// personne n'a validé.
func (v Version) Prereleased() (bool, bool) {
	if !v.KnowsPrerelease {
		return false, false
	}
	return v.Prerelease != "", true
}

// Scheme répond de la forme des tags d'un projet.
type Scheme interface {
	// ID nomme le schéma, tel qu'il est déclaré en configuration.
	ID() string
	// Parse décompose un tag, ou refuse celui qui n'a pas la forme attendue.
	Parse(tag string) (Version, error)
	// Mark attache un marqueur de promotion au tag, ou rend [ErrUnsupported].
	//
	// C'est la question qui décide de la compatibilité avec un déployeur : celui
	// qui sélectionne par métadonnée a besoin d'y écrire, et un schéma qui ne
	// sait pas en attacher rend cette promotion impossible.
	Mark(tag, marker string) (string, error)
	// Compare ordonne deux tags — négatif, nul, positif — ou rend
	// [ErrUnsupported]. Sans lui, l'interface affiche l'ordre de création plutôt
	// qu'un ordre de version, et le dit.
	Compare(a, b string) (int, error)
}

// Schemes énumère les schémas déclarables, dans l'ordre où l'écran les propose.
//
// Le calendaire, que le document cite, n'y est pas : aucun projet déclaré n'en
// emploie, et l'écrire d'avance reviendrait à deviner une convention — la faute
// que §12.4 décrit, du code qui compile et des verdicts faux. Il s'ajoutera avec
// le premier projet qui en a un, contre un cas réel.
func Schemes() []Scheme { return []Scheme{SemVer{}, Opaque{}} }

// For rend le schéma déclaré, ou une erreur nommant ceux qui existent.
//
// Vide vaut SemVer : c'est ce que tous les projets déclarés emploient, et exiger
// une déclaration explicite aurait rendu invalides des lignes que personne n'a
// écrites.
func For(id string) (Scheme, error) {
	if strings.TrimSpace(id) == "" {
		return SemVer{}, nil
	}
	for _, scheme := range Schemes() {
		if scheme.ID() == id {
			return scheme, nil
		}
	}

	known := make([]string, 0, len(Schemes()))
	for _, scheme := range Schemes() {
		known = append(known, scheme.ID())
	}
	return nil, fmt.Errorf("schéma de version inconnu %q (connus: %s)",
		id, strings.Join(known, ", "))
}

// semverPattern couvre la forme retenue : `MAJEUR.MINEUR.CORRECTIF` avec
// pré-release facultative. Les métadonnées de build (`+…`) sont volontairement
// absentes — le séparateur retenu pour la promotion est l'underscore, la
// grammaire des tags OCI excluant le `+` (B7).
var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// SemVer est le schéma qui répond à tout.
type SemVer struct{}

var _ Scheme = SemVer{}

func (SemVer) ID() string { return "semver" }

// Parse refuse plutôt que de tolérer : un tag hors forme casserait la jointure
// avec le suivi et la sélection par plage, et il vaut mieux le dire à la création
// que le découvrir en constatant qu'un environnement ne bouge pas.
func (SemVer) Parse(tag string) (Version, error) {
	matches := semverPattern.FindStringSubmatch(strings.TrimSpace(tag))
	if matches == nil {
		return Version{}, fmt.Errorf(
			"tag %q invalide: forme attendue MAJEUR.MINEUR.CORRECTIF[-prerelease]", tag)
	}

	return Version{
		Raw:             matches[0],
		Base:            fmt.Sprintf("%s.%s.%s", matches[1], matches[2], matches[3]),
		Prerelease:      matches[4],
		KnowsPrerelease: true,
	}, nil
}

// Mark suffixe le tag de son marqueur, séparé par un underscore.
//
// Underscore et non `+` : la grammaire des tags OCI exclut le second, alors que
// le SemVer le réserve à la métadonnée de build. La convention est donc du
// projet, pas du schéma — mais c'est le schéma qui sait où elle se pose.
func (s SemVer) Mark(tag, marker string) (string, error) {
	version, err := s.Parse(tag)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(marker) == "" {
		return version.Raw, nil
	}
	return version.Raw + "_" + marker, nil
}

// Compare ordonne par précédence SemVer.
//
// La règle qui compte, et qu'un tri lexical rate : une pré-release précède
// toujours sa version finale — `1.8.0-rc1` vient avant `1.8.0` —, alors que
// `-` trie après la chaîne vide dans l'autre sens.
func (s SemVer) Compare(a, b string) (int, error) {
	first, err := s.Parse(a)
	if err != nil {
		return 0, err
	}
	second, err := s.Parse(b)
	if err != nil {
		return 0, err
	}

	if order := compareNumericBase(first.Base, second.Base); order != 0 {
		return order, nil
	}

	switch {
	case first.Prerelease == second.Prerelease:
		return 0, nil
	case first.Prerelease == "":
		// Une finale suit toutes ses pré-releases.
		return 1, nil
	case second.Prerelease == "":
		return -1, nil
	}
	return strings.Compare(first.Prerelease, second.Prerelease), nil
}

// compareNumericBase compare deux lignes `X.Y.Z` composante par composante.
//
// Numériquement et non lexicalement : `1.10.0` suit `1.9.0`, alors que la
// comparaison de chaînes le place avant.
func compareNumericBase(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range left {
		if i >= len(right) {
			return 1
		}
		if order := compareNumbers(left[i], right[i]); order != 0 {
			return order
		}
	}
	if len(right) > len(left) {
		return -1
	}
	return 0
}

func compareNumbers(a, b string) int {
	// Les composantes viennent du motif : elles ne portent que des chiffres, et
	// comparer d'abord leur longueur suffit à les ordonner numériquement sans
	// conversion — donc sans débordement possible sur un numéro absurde.
	switch {
	case len(a) != len(b):
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// Opaque est le schéma qui n'affirme rien.
//
// Il **doit rester utilisable**, et c'est lui qui prouve le contrat : un schéma
// écrit sur la seule réalisation SemVer en aurait épousé la forme sans qu'on s'en
// aperçoive. Confronté à l'ignorance, le contrat a dû rendre chaque question
// faillible plutôt que de rendre des zéros — un `Base` vide et un `false` de
// pré-release auraient été indiscernables de réponses affirmatives.
type Opaque struct{}

var _ Scheme = Opaque{}

func (Opaque) ID() string { return "opaque" }

// Parse accepte tout tag non vide. « Tout l'est » : sans convention déclarée,
// rien ne permet de dire qu'un tag est mal formé, et le refuser serait imposer
// une règle que le projet n'a pas.
func (Opaque) Parse(tag string) (Version, error) {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return Version{}, errors.New("tag vide")
	}
	// `Base` reste vide et `KnowsPrerelease` faux : le tag ne se décompose pas.
	return Version{Raw: trimmed}, nil
}

// Mark refuse : attacher un marqueur suppose une grammaire, et l'appliquer à
// l'aveugle produirait un tag que le projet n'a pas choisi.
func (Opaque) Mark(string, string) (string, error) {
	return "", fmt.Errorf("%w: attacher un marqueur de promotion", ErrUnsupported)
}

// Compare refuse : deux tags opaques n'ont pas d'ordre connu. Un tri lexical
// serait un ordre **inventé**, et l'interface l'afficherait comme s'il était le
// bon.
func (Opaque) Compare(string, string) (int, error) {
	return 0, fmt.Errorf("%w: ordonner deux versions", ErrUnsupported)
}
