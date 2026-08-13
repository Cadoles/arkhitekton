// Package blob porte l'accès au stockage objet.
package blob

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Préfixes du bucket du dashboard. Le layout est décidé ici et nulle part
// ailleurs : c'est ce qui permet au dashboard de connaître la clé exacte de
// chaque artefact sans avoir à la chercher par préfixe.
const (
	prefixRaw       = "raw"
	prefixLogs      = "logs"
	prefixArtifacts = "artifacts"
)

// safeSegment n'accepte que ce qui compose légitimement un nom de PipelineRun,
// un tag de release ou un nom de fichier.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ErrUnsafeSegment signale un segment de clé refusé.
type ErrUnsafeSegment struct {
	Segment string
	Reason  string
}

func (e ErrUnsafeSegment) Error() string {
	return fmt.Sprintf("segment de clé refusé %q: %s", e.Segment, e.Reason)
}

// checkSegment valide un composant de clé.
//
// Les segments proviennent de noms de PipelineRun et de tags de release, ces
// derniers étant saisis par un utilisateur. Un tag valant "../../artifacts"
// écrirait hors du préfixe prévu — S3 n'a pas de notion de répertoire pour s'y
// opposer, la clé est une chaîne libre et "a/../b" y désigne littéralement un
// objet nommé ainsi. La validation est donc la seule barrière.
func checkSegment(segment string) error {
	switch {
	case segment == "":
		return ErrUnsafeSegment{segment, "vide"}
	case strings.Contains(segment, "/"):
		return ErrUnsafeSegment{segment, "contient une barre oblique"}
	case segment == "." || segment == "..":
		return ErrUnsafeSegment{segment, "référence de répertoire"}
	case !safeSegment.MatchString(segment):
		return ErrUnsafeSegment{segment, "caractères non autorisés"}
	}
	return nil
}

// join assemble une clé après validation de chaque segment.
func join(segments ...string) (string, error) {
	for _, segment := range segments {
		if err := checkSegment(segment); err != nil {
			return "", err
		}
	}
	return path.Join(segments...), nil
}

// Keys construit les clés du bucket. Le type existe pour que la connaissance du
// layout reste en un seul endroit : la dispersion de chaînes littérales dans le
// code est ce qui a produit, côté pipelines, des familles de tests rangées de
// quatre façons différentes.
type Keys struct{}

// RawArtifact désigne un artefact brut produit par un run.
//
// `runName` est le nom du PipelineRun : unique par construction, et directement
// corrélable au run qui l'a produit sans recherche ni convention datée.
func (Keys) RawArtifact(runName, artifact string) (string, error) {
	return join(prefixRaw, runName, artifact)
}

// RawMeta désigne le descripteur d'exécution d'un outil. Séparé des artefacts
// parce qu'il dit si le scanner a tourné, information qu'aucun rapport ne porte.
func (Keys) RawMeta(runName, tool string) (string, error) {
	return join(prefixRaw, runName, "meta", tool+".json")
}

// TaskLog désigne le log scellé d'une étape.
func (Keys) TaskLog(runName, taskName, step string) (string, error) {
	return join(prefixLogs, runName, taskName, step+".log")
}

// ReleaseArtifact désigne un livrable attaché à une release — bon de livraison,
// rapport de tests. Ces objets doivent survivre à la purge des artefacts bruts.
func (Keys) ReleaseArtifact(releaseTag, name string) (string, error) {
	return join(prefixArtifacts, "releases", releaseTag, name)
}

// RunPrefix rend le préfixe des artefacts bruts d'un run, pour un listing ou une
// purge.
func (Keys) RunPrefix(runName string) (string, error) {
	key, err := join(prefixRaw, runName)
	if err != nil {
		return "", err
	}
	return key + "/", nil
}
