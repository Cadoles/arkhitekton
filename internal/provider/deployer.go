package provider

import (
	"context"
	"errors"
)

/*
 * La frontière du déploiement.
 *
 * Écrite en dernier, et volontairement : les trois autres — suivi, forge,
 * attestation — sont des **lectures** dont le sens ne prête pas à discussion.
 * Celle-ci pose une question qui n'a pas de réponse universelle : que veut dire
 * « promouvoir » ?
 *
 * Chez MSE, cela veut dire re-taguer le même digest avec une métadonnée SemVer
 * que la plage d'une ImagePolicy retient, après quoi Flux constate et déploie —
 * le dashboard n'intervient jamais dans le cluster cible (B7). Un déployeur
 * ArgoCD, un `kubectl apply`, ou pas de déployeur du tout ne changent pas un
 * client HTTP : ils changent ce que *promu* signifie, donc quand une release
 * devient livrable, ce qu'on observe pour dire « déployé », et si l'on peut le
 * prouver.
 *
 * D'où un contrat en **questions**, et non en opérations. `Deploy(image, env)`
 * traduirait un protocole et perdrait le sens ; surtout, il n'autoriserait pas la
 * seule réponse honnête de certains déploiements : « je ne sais pas ».
 *
 * Ce paquet ne contient toujours aucun mécanisme de plugin (doc 12 §12.9, lot A).
 */

// Deployer répond à trois questions sur l'acheminement d'une version.
//
// Aucune ne demande au déployeur d'**agir** sur le cluster cible. Il décrit ce
// qu'il faut faire ; c'est le dashboard qui le fait passer par sa file, avec les
// pipelines qu'il possède. Cette séparation n'est pas une commodité : elle est ce
// qui rend l'action traçable, annulable et soumise aux mêmes droits que le reste,
// au lieu d'être un effet de bord enfoui dans une implémentation.
type Deployer interface {
	// Plan dit comment faire arriver cette version dans cet environnement, ou
	// pourquoi c'est impossible.
	//
	// Le refus est la partie qui compte. Chez MSE, pousser un tag qu'aucune
	// ImagePolicy ne retient ne produit **aucune erreur** : le registre accepte,
	// le pipeline réussit, et rien ne se déploie. C'est l'échec le plus coûteux
	// qui soit, parce qu'il ressemble à un succès. Un déployeur doit refuser en
	// amont ce qu'il sait sans effet.
	Plan(ctx context.Context, request Deployment) (Plan, error)

	// Observe rend ce qui tourne dans un environnement, et **avec quel niveau de
	// preuve**.
	//
	// Ne rend que ce que le déployeur sait lui-même : l'intention qu'il porte.
	// Le constat applicatif vient d'un [Attester], séparément, et c'est leur écart
	// qui fait la valeur de l'écran des environnements (B8). Les fondre ici ferait
	// disparaître exactement ce qu'on cherche à voir.
	Observe(ctx context.Context, target Target) (Observation, error)

	// Check dit si ce que Plan proposera aura un effet, avant que quiconque ne le
	// demande.
	//
	// Appelé au démarrage, sur tous les environnements déclarés. Un désaccord
	// empêche le démarrage plutôt que de laisser proposer des promotions sans
	// effet : l'écart ne se verrait sinon qu'en constatant qu'un environnement ne
	// bouge jamais (doc 06 §6.2).
	Check(ctx context.Context, targets []Target) error
}

// Target est un environnement, vu du dashboard.
//
// Deux champs seulement lui appartiennent en propre. Tout ce dont un déployeur a
// besoin en plus lui est passé dans son **propre vocabulaire** : le suffixe de
// promotion et l'acceptation des pré-releases n'ont de sens que pour une sélection
// par métadonnée SemVer, et les inscrire ici les imposerait à un déployeur qui ne
// sélectionne pas ainsi.
type Target struct {
	ID string
	// Observable dit si l'on peut interroger cet environnement. Faux pour la
	// production, dans son cluster indépendant. C'est un état distinct de
	// « inconnu » : sans la distinction, une carte figée ressemble à une panne.
	Observable bool
	// Settings porte ce que le déployeur a déclaré vouloir savoir de cet
	// environnement. Chaînes, parce que c'est ce qu'une configuration transporte,
	// et validées par Check plutôt que par le compilateur — le prix d'une
	// frontière que l'on veut franchissable par autre chose que du Go.
	Settings map[string]string
}

// Setting rend un réglage, ou la valeur par défaut fournie.
func (t Target) Setting(key, fallback string) string {
	if value, found := t.Settings[key]; found && value != "" {
		return value
	}
	return fallback
}

// Deployment est la demande adressée au déployeur.
type Deployment struct {
	Target Target
	// Version est l'identité de la release — son tag. C'est elle qui décide de
	// l'éligibilité à un environnement.
	Version string
	// Artifact est ce sous quoi la chose à déployer existe réellement. Distinct de
	// Version, et l'écart est le piège central : une release finale produite par
	// re-tag porte le tag `1.8.0` alors que son image existe sous `1.8.0-rc3`.
	// Les confondre donne soit un déploiement d'un artefact inexistant, soit une
	// demande que l'outil ignore (B17).
	Artifact string
	// Name est le composant à déployer — un dépôt d'image court. Une release en
	// publie plusieurs, et le déployeur est interrogé une fois par composant : lui
	// faire porter la liste supposerait qu'il sait ce qu'une release contient.
	Name string
}

// Plan décrit l'acheminement à réaliser.
//
// Exprimé dans la seule machinerie que le dashboard possède : lancer un pipeline.
// Tekton n'est pas un point d'extension, c'est une hypothèse assumée du produit,
// au même titre que PostgreSQL — le plan peut donc s'y appuyer sans rien perdre en
// généralité (doc 12 §12.5).
type Plan struct {
	// Kind est la famille de pipeline qui réalise l'acheminement.
	Kind string
	// Params sont ses paramètres.
	Params map[string]string
	// Reference identifie ce qui a été demandé, pour l'historique des promotions.
	// Chez MSE, le tag posé sur l'image.
	Reference string
}

// ErrNoDeployment signale qu'aucun acheminement n'est possible.
//
// Distinct d'une erreur de planification : ce n'est pas la demande qui est fautive,
// c'est qu'aucun outil ne sait la réaliser. L'interface doit le dire ainsi plutôt
// que d'afficher un échec, qui ferait chercher une panne là où il n'y a qu'une
// absence de configuration.
var ErrNoDeployment = errors.New("aucun déploiement possible")

// Resolution dit d'où vient ce qu'on affirme d'un environnement.
//
// C'est la distinction la plus utile de tout l'écran des environnements, et elle
// est fonctionnelle : attester qu'une version tourne n'est pas la même affirmation
// que constater le tag qu'un outil a retenu. Les confondre ferait annoncer
// « livré » un environnement dont les migrations ont échoué (B8).
//
// Les valeurs sont celles déjà écrites en base et déjà rendues par l'interface.
// Le contrat les **reprend** au lieu d'en inventer d'autres : un vocabulaire
// parallèle obligerait à une table de correspondance, qui divergerait.
type Resolution string

const (
	// ResolutionUnobservable : on ne peut rien affirmer.
	ResolutionUnobservable Resolution = "unobservable"
	// ResolutionDeclared : l'outil de déploiement dit ce qu'il a retenu. C'est une
	// intention, pas un constat — la valeur historique est `git`, du temps où le
	// seul outil était un GitOps.
	ResolutionDeclared Resolution = "git"
	// ResolutionAttested : l'application elle-même rapporte sa version, et que ses
	// migrations ont abouti. Seul niveau qui autorise « livré ».
	ResolutionAttested Resolution = "portaldata"
)

// Observation est ce qu'un déployeur sait d'un environnement.
type Observation struct {
	// Version est ce que l'outil de déploiement a retenu. Vide quand il ne sait
	// pas : une chaîne vide et « aucune version » sont ici la même chose, alors
	// qu'une valeur inventée serait indiscernable d'un constat.
	Version string
	// State est l'état rapporté, repris tel quel pour l'affichage. Le vocabulaire
	// appartient à l'outil.
	State string
	// Resolution dit ce que vaut ce qui précède.
	Resolution Resolution
}

// NoDeployer est le déployeur d'un projet qui n'en a pas.
//
// Pas un bouchon de test : c'est le comportement exact d'un déploiement où aucun
// outil n'est configuré, et c'est la réalisation qui **prouve le contrat**. Une
// interface écrite sur une seule implémentation en épouse toujours la forme sans
// qu'on s'en aperçoive ; celle-ci a été confrontée à l'absence, qui est le cas
// limite le plus révélateur — s'il ne s'exprime pas, l'interface parle
// d'opérations et non de questions (doc 12 §12.9, lot A).
//
// Ce qu'elle a fait bouger : `Plan` rend une erreur nommée plutôt qu'un plan vide,
// `Observe` rend une résolution explicite plutôt qu'un zéro, et `Check` accepte
// tout au lieu de valider un vocabulaire qu'il ne possède pas.
type NoDeployer struct{}

// Plan refuse, en disant que c'est l'outillage qui manque et non la demande.
func (NoDeployer) Plan(context.Context, Deployment) (Plan, error) {
	return Plan{}, ErrNoDeployment
}

// Observe n'affirme rien, et le dit.
func (NoDeployer) Observe(context.Context, Target) (Observation, error) {
	return Observation{Resolution: ResolutionUnobservable}, nil
}

// Check accepte tout : sans outil de déploiement, aucun environnement ne peut
// être en désaccord avec lui.
func (NoDeployer) Check(context.Context, []Target) error { return nil }

// La réalisation triviale honore le contrat, et l'assertion le tient à la
// compilation. Elle vaut autant que celle de Flux : une frontière que seule
// l'implémentation principale satisfait n'a pas été éprouvée.
var _ Deployer = NoDeployer{}
