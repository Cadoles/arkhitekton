// Package report produit les livrables attachés à une release.
//
// Le bon de livraison est un **document contractuel** : il atteste de ce qui a
// été livré, à quelle date, sur la foi de quelles vérifications. Deux
// conséquences pour ce paquet.
//
// D'abord, il n'invente rien. Chaque ligne du document renvoie à un fait
// enregistré — un digest lu dans le registre, une promotion demandée, un texte
// écrit par un humain identifié. Ce qui n'est pas connu est écrit « non
// disponible » et non omis : une absence silencieuse dans un document contractuel
// se lit comme une affirmation.
//
// Ensuite, la **mise en page** du bon de livraison n'est pas une décision
// technique. La doc 09 l'écrit : le format exact doit être validé avec le client
// avant développement. Ce paquet sépare donc strictement les deux : [Note] porte
// les faits et est stable ; le rendu PDF est provisoire et se remplace sans
// toucher à ce qui le nourrit.
package report

import (
	"encoding/json"
	"fmt"
	"time"
)

// Note est le contenu factuel d'un bon de livraison.
//
// Structure sérialisable telle quelle : le rendu JSON est un export à part
// entière, pas un intermédiaire. C'est même le seul format dont le contenu ne
// dépend d'aucune décision de présentation, donc le seul livrable sur lequel on
// puisse s'appuyer avant validation du format papier.
type Note struct {
	// Release est le tag livré, l'identité du document.
	Release string `json:"release"`
	// Version est le nom de la version Redmine correspondante. Égal à la base du
	// tag par construction, rappelé parce que c'est sous ce nom que le périmètre
	// fonctionnel est discuté.
	Version string `json:"version"`
	Kind    string `json:"kind"`
	// ImageVersion est la version portée par l'image. Distincte du tag pour toute
	// release finale produite par re-tag, et c'est **elle** qui sera constatée sur
	// les environnements.
	ImageVersion string `json:"image_version"`
	SourceTag    string `json:"source_release,omitempty"`
	GitRef       string `json:"git_ref,omitempty"`

	Images     []Image     `json:"images"`
	Promotions []Promotion `json:"promotions"`

	ManualActions *ManualActions `json:"manual_actions,omitempty"`

	// Unavailable énumère ce que le document ne peut pas attester, et pourquoi.
	// Écrit plutôt qu'omis : dans un document contractuel, une section absente se
	// lit comme une affirmation.
	Unavailable []string `json:"unavailable,omitempty"`

	GeneratedAt time.Time `json:"generated_at"`
	GeneratedBy string    `json:"generated_by"`
	// Provisional dit que la mise en page attend la validation du client. Porté
	// par les données et non par le seul rendu : le JSON doit le dire aussi, sans
	// quoi un intégrateur pourrait le traiter comme définitif.
	Provisional bool `json:"provisional"`
}

// Image est un tag et son empreinte.
type Image struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	// Digest vide signifie que le tag n'a pas pu être vérifié dans le registre.
	// Le document doit le dire : un digest est ce qui prouve que l'image livrée
	// est celle qui a été validée.
	Digest string `json:"digest,omitempty"`
}

// Promotion est un déploiement demandé.
type Promotion struct {
	Environment string    `json:"environment"`
	Tag         string    `json:"tag"`
	RequestedAt time.Time `json:"requested_at"`
	// Requested et non « déployé » : pousser un tag ne déploie rien, Flux
	// constate ensuite. Attester d'un déploiement sur la foi d'une promotion
	// serait une affirmation que le dashboard ne peut pas soutenir.
	Requested bool `json:"requested"`
}

// ManualActions est le récit des gestes manuels de la livraison.
type ManualActions struct {
	Markdown  string     `json:"markdown"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// KnownUnavailable nomme ce qui manquera au document tant que les lots
// correspondants ne sont pas livrés.
//
// Une constante plutôt qu'un texte glissé au rendu : la liste doit être la même
// dans le JSON et dans le PDF, et disparaître d'un seul geste le jour où la
// donnée existe.
var KnownUnavailable = []string{
	"Résultats de tests : la collecte des résultats n'est pas encore en place.",
	"Verdict de sécurité : la normalisation des findings n'est pas encore en place.",
	"Anomalies connues : la synchronisation Redmine n'est pas encore en place.",
}

// Validate refuse un bon de livraison qui n'atteste de rien.
//
// Le contrôle est volontairement minimal — le document doit pouvoir être produit
// tôt, précisément pour être relu — mais il refuse l'absence d'identité : un bon
// de livraison sans release ni version ne désigne aucune livraison.
func (n Note) Validate() error {
	if n.Release == "" {
		return fmt.Errorf("bon de livraison sans release")
	}
	if n.ImageVersion == "" {
		return fmt.Errorf("bon de livraison de %q sans version d'image: rien n'identifie ce qui est livré", n.Release)
	}
	return nil
}

// UnverifiedImages rend les tags dont l'empreinte n'a pas pu être lue.
//
// Employé par les deux rendus : le document doit nommer ce qu'il n'a pas pu
// vérifier, à l'endroit où le lecteur cherche la preuve.
func (n Note) UnverifiedImages() []string {
	var unverified []string
	for _, image := range n.Images {
		if image.Digest == "" {
			unverified = append(unverified, image.Tag)
		}
	}
	return unverified
}

// RenderJSON rend le bon de livraison en JSON.
//
// Indenté : ce fichier est lu par des humains autant que par des programmes, et
// il peut être joint à un échange contractuel.
func RenderJSON(note Note) ([]byte, error) {
	if err := note.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(note, "", "  ")
}
