// Package pipelines porte la bibliothèque de pipelines que le dashboard installe
// lui-même, et le mécanisme qui la pose.
//
// # Deux tiers, une seule raison
//
// Le **noyau** — construire, re-taguer — vit dans le `deploy/` du dashboard et
// arrive avec lui, comme le Job de migration. Il ne suppose aucune pile, et sa
// version suit celle du binaire (doc 12 §12.6).
//
// Les implémentations de **référence** supposent une pile, et c'est pourquoi
// elles ne peuvent pas y vivre : un projet Go n'a que faire des Tasks PHP. Elles
// s'installent donc **à la demande**, quand un projet déclare la sienne, ce qui
// est exactement le critère de §12.7 — l'état désiré change quand quelqu'un agit
// dans l'interface.
//
// # Ce que ce paquet n'est pas
//
// Ni un gestionnaire de paquets, ni un mécanisme d'extension. Les bibliothèques
// sont **embarquées dans le binaire** : rien n'est téléchargé, rien n'est
// résolu à l'exécution, et la version d'une bibliothèque est celle du dashboard
// qui la pose. Le jour où un projet a besoin d'autre chose, il **forke** — le
// normatif est le contrat de doc 13, pas cette implémentation.
//
// # Ce que le dashboard possède, et ce qu'il refuse
//
// Il ne pose que des ressources qu'il **nomme**, marquées `managed-by`, et
// n'**adopte jamais** ce qui existe déjà. C'est la même règle que pour les
// secrets du lot G, et elle a ici une conséquence heureuse : sur un cluster où
// `mse-tekton` a déjà posé ses pipelines, le dashboard constate et se retire, au
// lieu d'écraser des définitions que quelqu'un d'autre maintient.
//
// Ce mécanisme sert donc l'**autonomie** telle que le jalon de vérification la
// définit : une instance qui n'a eu besoin d'aucun dépôt de pipelines
// préexistant.
package pipelines

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// library porte les bibliothèques de référence, une par pile.
//
//go:embed library
var library embed.FS

// Resource est un objet Tekton à poser, tel qu'il est écrit dans la
// bibliothèque.
type Resource struct {
	// Name est le nom de l'objet, lu dans son manifeste. Sert à dire ce qui a
	// été posé, et ce qui a été refusé.
	Name string
	// Kind est `Pipeline` ou `Task`.
	Kind string
	// Manifest est le YAML tel quel. Non réécrit : le dashboard pose ce que la
	// bibliothèque déclare, et un manifeste modifié à la volée serait une
	// définition que personne n'a relue.
	Manifest []byte
}

// Stacks énumère les piles dont une bibliothèque existe.
//
// Une seule aujourd'hui, et c'est délibéré. `php` est extraite de ce qui tourne
// réellement — les Tasks et Pipelines de `mse-tekton`, éprouvés par des mois
// d'exécution. Écrire d'avance une bibliothèque Node ou Go reviendrait à deviner
// un lanceur de tests, des chemins de sortie et une émission de marqueurs, c'est-
// à-dire à produire du code qui compile et des verdicts faux (doc 12 §12.4).
//
// Elles s'ajouteront contre un projet réel. En attendant, un projet dont la pile
// n'est pas couverte **écrit les siennes** et n'a qu'un contrat à honorer : c'est
// ce que doc 12 §12.6 appelle « installables et forkables ».
func Stacks() []string {
	entries, err := fs.ReadDir(library, "library")
	if err != nil {
		return nil
	}

	stacks := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			stacks = append(stacks, entry.Name())
		}
	}
	sort.Strings(stacks)
	return stacks
}

// ErrUnknownStack signale une pile dont aucune bibliothèque n'existe.
//
// Nommée plutôt que silencieuse : un projet qui déclare `node` aujourd'hui doit
// l'apprendre à la déclaration, et non constater que ses pipelines n'existent
// pas au premier lancement.
type ErrUnknownStack struct {
	Stack string
	Known []string
}

func (e ErrUnknownStack) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("pile %q inconnue: aucune bibliothèque de référence n'est embarquée",
			e.Stack)
	}
	return fmt.Sprintf(
		"pile %q inconnue (embarquées: %s) — un projet dont la pile n'est pas couverte "+
			"écrit ses propres pipelines et n'a que le contrat à honorer",
		e.Stack, strings.Join(e.Known, ", "))
}

// For rend les ressources de la bibliothèque d'une pile.
//
// Une pile vide rend une liste vide sans erreur : c'est le cas d'un projet qui
// apporte ses propres pipelines, et il est parfaitement valide.
func For(stack string) ([]Resource, error) {
	stack = strings.TrimSpace(stack)
	if stack == "" {
		return nil, nil
	}

	entries, err := fs.ReadDir(library, path.Join("library", stack))
	if err != nil {
		return nil, ErrUnknownStack{Stack: stack, Known: Stacks()}
	}

	resources := make([]Resource, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		manifest, err := library.ReadFile(path.Join("library", stack, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("lecture de %s/%s: %w", stack, entry.Name(), err)
		}

		name, kind, err := identify(manifest)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", stack, entry.Name(), err)
		}
		resources = append(resources, Resource{Name: name, Kind: kind, Manifest: manifest})
	}
	return resources, nil
}

// identify lit le nom et le type d'un manifeste.
//
// Une lecture ligne à ligne plutôt qu'un décodage YAML complet : on ne cherche
// que deux champs de tête, et décoder l'objet entier obligerait à connaître le
// schéma de Tekton pour rien — le manifeste est posé tel quel.
func identify(manifest []byte) (name, kind string, err error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case kind == "" && strings.HasPrefix(trimmed, "kind:"):
			kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
		case name == "" && strings.HasPrefix(line, "  name:"):
			// Indenté de deux espaces : c'est `metadata.name`, et non le `name`
			// d'un paramètre ou d'une étape, qui vivent plus profond.
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		}
		if name != "" && kind != "" {
			break
		}
	}

	if name == "" || kind == "" {
		return "", "", fmt.Errorf("manifeste sans nom ou sans type (lu: %q/%q)", kind, name)
	}
	return name, kind, nil
}
