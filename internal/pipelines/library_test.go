package pipelines

import (
	"errors"
	"strings"
	"testing"
)

// La bibliothèque PHP est extraite de ce qui tourne réellement. Ce test dit ce
// qu'elle doit contenir : si un manifeste disparaît de l'embarquement — un
// fichier renommé, un répertoire déplacé — rien d'autre ne le signalerait avant
// qu'un pipeline ne soit introuvable dans le cluster.
func TestLaBibliothequePhpPorteSesPipelinesEtSesTaches(t *testing.T) {
	resources, err := For("php")
	if err != nil {
		t.Fatalf("bibliothèque php: %v", err)
	}

	byName := map[string]Resource{}
	for _, resource := range resources {
		byName[resource.Name] = resource
	}

	for _, name := range []string{"php-unit-tests", "php-static-tests", "php-integration-test"} {
		resource, found := byName[name]
		if !found {
			t.Errorf("pipeline %q absent de la bibliothèque", name)
			continue
		}
		if resource.Kind != "Pipeline" {
			t.Errorf("%q est un %q", name, resource.Kind)
		}
	}

	for _, name := range []string{"unit-test", "phpstan", "cs-fixer", "integration-test"} {
		resource, found := byName[name]
		if !found {
			t.Errorf("task %q absente de la bibliothèque", name)
			continue
		}
		if resource.Kind != "Task" {
			t.Errorf("%q est un %q", name, resource.Kind)
		}
	}
}

// Le manifeste est posé **tel quel**. Le réécrire à la volée produirait une
// définition que personne n'a relue, et qui divergerait de ce que le dépôt
// montre.
func TestLeManifesteEstRenduTelQuel(t *testing.T) {
	resources, _ := For("php")
	if len(resources) == 0 {
		t.Fatal("bibliothèque vide")
	}

	for _, resource := range resources {
		if !strings.Contains(string(resource.Manifest), "apiVersion: tekton.dev/") {
			t.Errorf("%q: le manifeste ne ressemble pas à un objet Tekton", resource.Name)
		}
	}
}

// Une pile vide est parfaitement valide : c'est le projet qui apporte ses
// propres pipelines, et le refuser lui interdirait d'exister.
func TestUnePileVideNestPasUneErreur(t *testing.T) {
	resources, err := For("")
	if err != nil {
		t.Fatalf("pile vide: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("%d ressource(s) pour une pile vide", len(resources))
	}
}

// Une pile inconnue est nommée à la déclaration, et le message dit quoi faire :
// forker. C'est ce que doc 12 §12.6 appelle « installables et forkables ».
func TestUnePileInconnueEstNommeeAvecCellesQuiExistent(t *testing.T) {
	_, err := For("node")

	var unknown ErrUnknownStack
	if !errors.As(err, &unknown) {
		t.Fatalf("erreur %v, attendu ErrUnknownStack", err)
	}
	if !strings.Contains(err.Error(), "php") {
		t.Errorf("message %q — il doit nommer les piles embarquées", err)
	}
	if !strings.Contains(err.Error(), "contrat") {
		t.Errorf("message %q — il doit dire qu'un projet peut écrire les siens", err)
	}
}

// Les piles embarquées se découvrent du système de fichiers : en ajouter une
// n'est qu'un répertoire à déposer, pas une liste à tenir à jour.
func TestLesPilesSeDecouvrentDeLEmbarquement(t *testing.T) {
	stacks := Stacks()
	if len(stacks) == 0 {
		t.Fatal("aucune pile embarquée")
	}
	if stacks[0] != "php" {
		t.Errorf("piles %v — php doit y être", stacks)
	}
}
