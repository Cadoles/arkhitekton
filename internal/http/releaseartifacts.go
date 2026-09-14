package http

import (
	"context"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Ce que l'automatisation d'une release a produit.
 *
 * # Ce que cette section remplace
 *
 * L'écran listait des « images » : **un seul dépôt**, celui que la configuration
 * nomme, décliné en ses tags — construction, release, promotions. Un projet qui
 * en publie trois n'en voyait qu'un, et un projet qui produit un binaire ne
 * voyait rien du tout.
 *
 * Elle liste désormais ce que les gestes ont réellement rendu, relevé à leur
 * conclusion depuis les résultats Tekton (doc 15 §15.7). Trois constructions,
 * trois lignes, nommées par le geste qui les a produites.
 *
 * # Le contrôle de digest est conservé, et élargi
 *
 * L'ancienne section prouvait qu'une promotion n'avait pas reconstruit : le tag
 * promu et le tag d'origine doivent porter le **même** digest (B17). Cette
 * garantie ne se perd pas — elle s'applique maintenant à chaque image produite,
 * au lieu du seul dépôt configuré.
 */

// planArtifactPayload est un artefact produit, tel que l'écran le lit.
type planArtifactPayload struct {
	// Geste nomme ce qui l'a produit, dans les mots de la déclaration.
	Geste string `json:"geste"`
	// Name est le nom du résultat rendu par le pipeline : `image`, `tag`.
	Name string `json:"name"`
	// Kind reprend le vocabulaire de `produces` du catalogue.
	//
	// Descriptif et volontairement pas structurant : il vaut `image` pour tous les
	// résultats d'un pipeline qui produit des images, y compris pour un `tag` qui
	// n'en est pas une. L'écran se fie au **nom**, qui est juste.
	Kind string `json:"kind,omitempty"`
	// Reference désigne l'artefact : pour une image, sa référence complète.
	Reference string `json:"reference"`
	// Digest prouve ce qui a été publié. Vide quand le registre n'est pas
	// interrogeable, ou quand l'artefact n'est pas une image.
	Digest string `json:"digest,omitempty"`
	// Diverged dit qu'un tag de promotion porte un digest différent de celui-ci —
	// donc qu'une image a été reconstruite entre la validation et la livraison.
	Diverged bool `json:"diverged,omitempty"`
}

// releaseArtifacts compose ce que l'automatisation a produit pour cette release.
//
// Un artefact dont l'exécution n'est plus lisible garde son nom de geste vide
// plutôt que de disparaître : c'est ce qui a été publié, et la perte du plan
// figé n'y change rien.
func (h *ReleasesHandler) releaseArtifacts(
	ctx context.Context, releaseID int64, promotions []store.Promotion,
) []planArtifactPayload {
	if h.artifactReader == nil {
		return nil
	}

	artifacts, err := h.artifactReader.ReleaseArtifacts(ctx, releaseID)
	if err != nil {
		// Journalisé et non rendu : un artefact illisible ne doit pas rendre la
		// page d'une release inaccessible. Ce qui reste s'affiche.
		h.logger.Error("artefacts d'une release", "release", releaseID, "error", err)
		return nil
	}

	// Les libellés des gestes viennent du plan **figé** de l'exécution qui a
	// produit l'artefact. Une automatisation renommée depuis ne réécrit donc pas
	// l'histoire : la ligne porte le nom qu'elle avait ce jour-là.
	labels := map[int64]map[store.GesteRef]string{}

	payload := make([]planArtifactPayload, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, lu := labels[artifact.RunID]; !lu {
			labels[artifact.RunID] = h.gesteLabels(ctx, artifact.RunID)
		}
		ref := store.GesteRef{Stage: artifact.Stage, Position: artifact.Position}

		entry := planArtifactPayload{
			Geste: labels[artifact.RunID][ref], Name: artifact.Name,
			Kind: artifact.Kind, Reference: artifact.Reference,
		}
		// Seules les images ont un digest. Le nom fait foi et non `kind`, qui vaut
		// `image` pour tout ce qu'un pipeline d'images rend — le tag compris.
		if artifact.Name == "image" {
			entry.Digest, entry.Diverged = h.digestOf(ctx, artifact.Reference, promotions)
		}
		payload = append(payload, entry)
	}
	return payload
}

// gesteLabels rend le nom de chaque geste du plan figé d'une exécution.
func (h *ReleasesHandler) gesteLabels(
	ctx context.Context, runID int64,
) map[store.GesteRef]string {
	labels := map[store.GesteRef]string{}

	run, err := h.artifactReader.PlanRunByID(ctx, runID)
	if err != nil {
		// L'exécution a pu être purgée. L'artefact reste vrai et s'affiche sans
		// le nom de ce qui l'a produit.
		return labels
	}
	for stage, entry := range run.Plan.Stages {
		for position, geste := range entry.Gestes {
			labels[store.GesteRef{Stage: stage, Position: position}] = geste.Name()
		}
	}
	return labels
}

// digestOf lit le digest d'une image publiée, et dit s'il a divergé.
//
// La divergence est l'invariant de livraison : un tag de promotion doit porter le
// digest de l'image validée. Un écart signale une image reconstruite entre-temps,
// c'est-à-dire une livraison qui n'est plus celle qui a été validée (B17).
func (h *ReleasesHandler) digestOf(
	ctx context.Context, reference string, promotions []store.Promotion,
) (string, bool) {
	if h.images == nil {
		return "", false
	}
	repository, tag, ok := splitReference(reference)
	if !ok {
		return "", false
	}

	digest, err := h.images.Digest(ctx, repository, tag)
	if err != nil || digest == "" {
		return "", false
	}

	for _, promotion := range promotions {
		promoted, err := h.images.Digest(ctx, repository, promotion.Tag)
		if err != nil || promoted == "" {
			// Un tag absent du registre n'est pas une divergence : la promotion
			// n'a peut-être pas encore abouti. L'annoncer comme un écart enverrait
			// chercher une reconstruction là où il n'y a qu'une attente.
			continue
		}
		if promoted != digest {
			return digest, true
		}
	}
	return digest, false
}

// splitReference sépare le dépôt court et le tag d'une référence complète.
//
// Le dépôt **court** parce que c'est ce que le client de registre attend : il
// porte déjà son organisation. `reg.example.fr/vcarroy/portal:1.0` rend donc
// `portal` et `1.0`.
func splitReference(reference string) (repository, tag string, ok bool) {
	colon := strings.LastIndex(reference, ":")
	slash := strings.LastIndex(reference, "/")
	if colon < 0 || colon < slash {
		return "", "", false
	}
	path, tag := reference[:colon], reference[colon+1:]
	if slash >= 0 {
		path = path[slash+1:]
	}
	if path == "" || tag == "" {
		return "", "", false
	}
	return path, tag, true
}
