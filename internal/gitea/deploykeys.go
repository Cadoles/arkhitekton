package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

/*
 * Les clés de déploiement déclarées sur un dépôt.
 *
 * Lues pour répondre à une seule question : la clé que le dashboard a produite
 * pour ce projet y est-elle posée ? C'est le geste que le dashboard ne peut pas
 * faire lui-même — coller la moitié publique dans les réglages d'un dépôt
 * appartient à un humain — et le **constater** est ce qui évite de le lui faire
 * confirmer à la main.
 *
 * La distinction est celle de B8, appliquée à un autre objet : une confirmation
 * humaine est une **intention**, la lecture de la forge est un **constat**. La
 * première pouvait rester vraie après qu'on a retiré la clé du dépôt, et les
 * pipelines échouaient alors sur une erreur SSH que rien ne rattachait à
 * l'écran qui affirmait le contraire.
 */

// deployKey est ce que Gitea rend pour une clé de dépôt.
type deployKey struct {
	// Key porte le type, la matière et parfois un commentaire, séparés par des
	// espaces : `ssh-ed25519 AAAAC3Nz… mse-dashboard`.
	Key   string `json:"key"`
	Title string `json:"title"`
}

// DeployKeys rend les clés de déploiement déclarées sur le dépôt du client.
//
// Le jeton doit avoir un droit d'**administration** sur le dépôt : Gitea réserve
// cet endpoint, et un jeton de lecture seule reçoit 403. L'appelant doit traiter
// l'échec comme « je ne sais pas » et non comme « la clé est absente » — les deux
// n'appellent pas la même réaction.
func (c *Client) DeployKeys(ctx context.Context) ([]string, error) {
	var keys []string

	// Paginé comme les références : un dépôt peut porter plus de clés qu'une page,
	// et s'arrêter à la première ferait conclure « absente » sur une clé qui vient
	// après.
	for page := 1; len(keys) < maxDeployKeys; page++ {
		query := url.Values{
			"page":  {strconv.Itoa(page)},
			"limit": {strconv.Itoa(pageSize)},
		}
		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/keys", query)
		if err != nil {
			return nil, err
		}

		var batch []deployKey
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("gitea: clés de déploiement illisibles: %w", err)
		}
		for _, entry := range batch {
			keys = append(keys, entry.Key)
		}
		if len(batch) < pageSize {
			break
		}
	}
	return keys, nil
}

// maxDeployKeys borne la lecture. Un dépôt qui en porterait davantage a un autre
// problème que celui qu'on cherche à diagnostiquer ici.
const maxDeployKeys = 200

// SameKey dit si deux clés publiques SSH désignent la même paire.
//
// La comparaison porte sur le **type et la matière**, jamais sur le commentaire :
// celui-ci est libre, souvent réécrit par l'interface de la forge au moment du
// collage, et comparer les chaînes entières ferait répondre « absente » sur une
// clé pourtant en place. C'est exactement le genre de faux négatif qui enverrait
// quelqu'un recoller une clé déjà posée.
func SameKey(a, b string) bool {
	typeA, matiereA, okA := splitKey(a)
	typeB, matiereB, okB := splitKey(b)
	return okA && okB && typeA == typeB && matiereA == matiereB
}

// splitKey isole le type et la matière d'une clé publique.
func splitKey(key string) (keyType, material string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}
