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

// TaskLogObject désigne le log scellé d'une task : la concaténation de ses étapes
// dans l'ordre d'exécution.
//
// Un seul objet par task, et non un par étape, parce que c'est ce qu'on lit :
// « le log de cette task ». Les étapes d'un TaskRun s'exécutent séquentiellement,
// leur concaténation est donc chronologique.
func (Keys) TaskLogObject(runName, taskName string) (string, error) {
	key, err := join(prefixLogs, runName, taskName)
	if err != nil {
		return "", err
	}
	return key + ".log", nil
}

// TaskLogChunk désigne une tranche de log, écrite pendant l'exécution.
//
// Les tranches existent parce que les logs Tekton disparaissent avec les pods : il
// faut les rendre durables au fil de l'eau, sans attendre la fin de la task. Un
// objet par tranche, et non un envoi multipart : les parties d'un envoi multipart
// S3 doivent peser au moins 5 Mio, ce qui interdit une granularité de flush
// utile — un incident perdrait jusqu'à 5 Mio de journal.
//
// Deux numéros sont zéro-paddés, et c'est ce qui fait tout l'intérêt de ce
// layout : **l'ordre lexicographique des clés est l'ordre chronologique du
// journal**, sans rien connaître du pipeline.
//
//   - `index` est le rang de l'étape dans la task. Sans lui, l'ordre serait
//     alphabétique par nom de conteneur : une task qui exécute `run-tests` puis
//     `publish-results` verrait sa publication classée avant ses tests, puisque
//     `step-publish-results` précède `step-run-tests` dans l'alphabet.
//   - `sequence` est le rang de la tranche dans l'étape, sans quoi la tranche 10
//     précéderait la tranche 2.
//
// La conséquence pratique est qu'un simple listing trié suffit à lire un journal
// en cours, avant tout scellement.
func (Keys) TaskLogChunk(runName, taskName, step string, index, sequence int) (string, error) {
	if index < 0 || sequence < 0 {
		return "", ErrUnsafeSegment{
			Segment: fmt.Sprintf("%d/%d", index, sequence),
			Reason:  "rang d'étape ou numéro de tranche négatif",
		}
	}
	prefix, err := taskLogChunkPrefix(runName, taskName, step, index)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%08d.log", prefix, sequence), nil
}

// TaskLogChunkPrefix rend le préfixe des tranches d'une étape, pour les lister au
// redémarrage ou les purger après scellement.
func (Keys) TaskLogChunkPrefix(runName, taskName, step string, index int) (string, error) {
	return taskLogChunkPrefix(runName, taskName, step, index)
}

func taskLogChunkPrefix(runName, taskName, step string, index int) (string, error) {
	if index < 0 {
		return "", ErrUnsafeSegment{Segment: fmt.Sprint(index), Reason: "rang d'étape négatif"}
	}
	key, err := join(prefixLogs, runName, taskName, "parts", fmt.Sprintf("%03d-%s", index, step))
	if err != nil {
		return "", err
	}
	return key + "/", nil
}

// TaskLogPartsPrefix rend le préfixe de toutes les tranches d'une task.
func (Keys) TaskLogPartsPrefix(runName, taskName string) (string, error) {
	key, err := join(prefixLogs, runName, taskName, "parts")
	if err != nil {
		return "", err
	}
	return key + "/", nil
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
