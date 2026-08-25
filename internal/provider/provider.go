// Package provider nomme les frontières avec les outils extérieurs.
//
// Le dashboard lit trois sources qu'il ne pilote pas : un **suivi** de versions
// et de demandes, une **forge** de dépôts et de références, et une **attestation**
// de ce qui tourne réellement. Chacune a aujourd'hui une seule réalisation —
// Redmine, Gitea, le CRD `PortalData` — et ce paquet ne cherche pas à en ajouter.
//
// Ce qu'il apporte est plus modeste et plus utile : un endroit qui dit ce qu'être
// une de ces sources **veut dire**. Jusqu'ici la réponse était éparpillée dans
// une demi-douzaine d'interfaces étroites déclarées par leurs appelants — bon
// idiome Go, mais personne ne pouvait dire, sans les lire toutes, ce qu'un second
// suivi devrait fournir. Le coût de cette dispersion ne se paie pas au moment du
// découplage : il se paie à chaque ajout, quand une méthode propre à Redmine
// entre dans un handler sans que rien ne le signale (doc 12 §12.9, chantier 4).
//
// # Ce que ce paquet n'est pas
//
// Pas un mécanisme de plugin. Aucun registre, aucun `init()`, aucune sélection à
// l'exécution. La configuration désigne toujours Redmine et Gitea en clair, et le
// binaire ne sait construire qu'eux. Le jour où un second fournisseur existera,
// c'est ici qu'on saura quoi lui demander — pas avant.
//
// Pas non plus une couche d'indirection à traverser. Les appelants continuent de
// déclarer l'interface étroite dont ils ont besoin, et de recevoir le client
// concret. Ce paquet porte le **vocabulaire** qu'ils échangent et le contrat
// complet que chaque client honore ; les assertions de conformité, placées auprès
// de chaque réalisation, font que le contrat casse à la compilation plutôt que de
// diverger en silence.
//
// # Ce qui manque, et pourquoi
//
// `Deployer` — promouvoir, observer, prouver — n'y est pas. Il exige de répondre
// à « que veut dire promouvoir », et cette réponse est précisément ce que
// l'analyse a montré ne pas pouvoir se trancher dans l'abstrait (doc 12 §12.4).
// L'écrire maintenant produirait une interface calquée sur Flux, qu'on croirait
// générique.
//
// `Exporter` non plus : le bon de livraison est un livrable contractuel dont la
// forme appartient au projet, et il n'a pas de source extérieure à abstraire.
package provider

import (
	"context"
	"strconv"
	"time"
)

// Account est l'identité derrière une clé d'accès.
//
// Rendue par les deux sources authentifiées, pour la même raison : savoir qu'une
// clé fonctionne sans attendre qu'un formulaire s'en serve, et voir si elle
// appartient à un compte d'administration. Un jeton trop puissant reste trop
// puissant même inutilisé, et mieux vaut le savoir au démarrage.
type Account struct {
	// ID est l'identifiant numérique quand la source en expose un. Zéro sinon —
	// Gitea n'en rend pas sur `/user`, et inventer une valeur ferait croire à une
	// identité qu'on n'a pas lue.
	ID    int64
	Login string
	Admin bool
}

// Tracker est la source des versions et des demandes.
//
// Lecture seule, et c'est structurant : le dashboard n'écrit jamais dans le
// suivi. Il rapproche ce qu'il y lit de ce qu'il observe, et laisse la correction
// à l'outil qui fait autorité. Une réalisation qui écrirait sortirait du contrat.
type Tracker interface {
	// ListVersions rend les versions déclarées d'un projet.
	ListVersions(ctx context.Context, project string) ([]Version, error)
	// ListIssues rend les demandes correspondant à la requête, toutes pages
	// réunies. La pagination appartient à la réalisation : l'appelant demande un
	// périmètre, pas une page.
	ListIssues(ctx context.Context, query IssueQuery) ([]Issue, error)
	// CheckAccess valide la clé et rend le compte auquel elle appartient. Doit
	// être sans effet de bord : elle est appelée au démarrage, et un contrôle qui
	// modifierait quelque chose rendrait le démarrage observable de l'extérieur.
	CheckAccess(ctx context.Context) (Account, error)
}

// Version est une version déclarée dans le suivi.
//
// `Name` est conservé tel quel. L'instance réelle en porte quatre conventions
// simultanées — `MSE3-1.5`, `MSE 3.1.0`, `v3.2.0` — et normaliser à l'écriture
// perdrait l'information sans rien résoudre. L'identité est `ID`, jamais le nom.
type Version struct {
	ID          int64
	Name        string
	Description string
	// Status vaut `open`, `locked` ou `closed`. Vocabulaire de Redmine, repris
	// tel quel : le dashboard n'en fait qu'un affichage et un tri, et le traduire
	// dans un vocabulaire à lui créerait une table de correspondance à tenir pour
	// n'apporter aucune information.
	Status    string
	DueDate   *time.Time
	UpdatedOn *time.Time
}

// Issue est une demande, réduite à ce que le dashboard affiche.
//
// La description n'est pas reprise : elle porte le détail métier, parfois
// nominatif, et le dashboard renvoie vers le suivi pour la consulter. Ne pas
// recopier ce dont on n'a pas besoin est la façon la plus simple de ne pas avoir
// à le protéger.
type Issue struct {
	ID        int64
	VersionID *int64
	Tracker   string
	Subject   string
	Status    string
	// StatusClosed est le drapeau de la source, repris sans jugement. La
	// projection en `todo`/`doing`/`done`/`rejected` est une décision du
	// dashboard, portée par une table éditable, et les deux peuvent diverger :
	// l'instance marque `Ré-ouvert` comme clos, ce qui ressemble à une erreur de
	// configuration.
	StatusClosed bool
	Priority     string
	Assignee     string
	DoneRatio    int
	UpdatedOn    *time.Time
}

// IssueQuery borne une lecture de demandes.
type IssueQuery struct {
	// Project est l'identifiant ou le raccourci du projet.
	Project string
	// Since restreint aux demandes modifiées depuis cet instant. Zéro pour tout
	// lire, ce qui n'arrive qu'à la première synchronisation.
	Since time.Time
}

// Forge est la source des dépôts, des références et des demandes de fusion.
//
// Liée à **un** dépôt, celui de la configuration : les méthodes n'en prennent pas
// le nom en paramètre. Tester un dépôt qu'on vient de déclarer relève de
// [RepositoryProber], qui n'a pas cette contrainte.
//
// En **lecture** uniquement. Écrire — poser un tag — est une permission
// distincte, portée par [TagWriter] et par un autre client : la séparation existe
// pour qu'un déploiement puisse configurer la lecture sans jamais accorder
// l'écriture, et la fondre ici la ferait disparaître.
type Forge interface {
	// DefaultBranch rend la branche par défaut du dépôt.
	DefaultBranch(ctx context.Context) (string, error)
	// ListBranches et ListTags rendent les références et disent si la borne de
	// lecture a été atteinte. Le second retour n'est pas décoratif : une liste
	// tronquée en silence ferait croire qu'une référence n'existe pas.
	ListBranches(ctx context.Context) ([]Ref, bool, error)
	ListTags(ctx context.Context) ([]Ref, bool, error)
	// ResolveCommit résout une référence en SHA de commit.
	ResolveCommit(ctx context.Context, ref string) (string, error)
	// ListMergedPullRequests rend les demandes fusionnées depuis un instant, de
	// la plus récente à la plus ancienne.
	ListMergedPullRequests(ctx context.Context, since time.Time) ([]PullRequest, error)
	// LoadCommitMessages complète une demande par les messages de ses commits.
	// Séparé de la liste parce que c'est un aller-retour par demande : les lire
	// d'office coûterait des centaines de requêtes pour un rapprochement qui n'en
	// demande que quelques-unes.
	LoadCommitMessages(ctx context.Context, pull *PullRequest) error
	// CheckAccess valide le jeton et rend le compte auquel il appartient.
	CheckAccess(ctx context.Context) (Account, error)
}

// RepositoryProber teste l'accès à un dépôt **arbitraire** et rend sa branche
// par défaut.
//
// Hors de [Forge], et pas par commodité : une Forge est liée à un dépôt à sa
// construction, alors que le test porte justement sur un dépôt qu'on vient de
// déclarer et dont on ne sait pas encore s'il est joignable. Les fondre
// obligerait à construire un client pour découvrir qu'on ne peut pas.
type RepositoryProber interface {
	// Probe rend la branche par défaut, ou l'erreur qui l'en empêche. Un dépôt
	// sans branche par défaut existe mais n'a jamais reçu de commit : c'est un
	// échec, parce que le clone d'un pipeline y échouerait plus tard.
	Probe(ctx context.Context, repository string) (string, error)
}

// TagWriter pose un tag Git (B3).
//
// Séparée de [Forge] : lire la forge et y écrire sont deux permissions, portées
// par deux clients. L'implémentation est nulle quand le jeton ne peut pas écrire,
// et le dashboard sait alors le dire au lieu d'échouer au moment du tag.
type TagWriter interface {
	CreateTag(ctx context.Context, tag, commit, message string) error
}

// Ref est une branche ou un tag.
type Ref struct {
	Name string
	// SHA du commit visé. Informatif : le champ `git_ref` d'une release porte le
	// nom, la résolution en commit appartient au pipeline au moment du build.
	SHA string
}

// PullRequest est une demande de fusion aboutie, réduite à ce qui sert au
// rapprochement avec les demandes du suivi.
type PullRequest struct {
	Number   int64
	Title    string
	Body     string
	MergedAt *time.Time
	// Commits porte les messages des commits. Renseigné par LoadCommitMessages :
	// la liste des demandes ne les contient pas.
	Commits []string
}

// Reference rend `#42`, tel qu'on l'écrit dans une note.
func (p PullRequest) Reference() string {
	return "#" + strconv.FormatInt(p.Number, 10)
}

// Attester dit quelle version tourne réellement dans un namespace.
//
// Séparé de la promotion **délibérément**, et c'est la distinction qui fait la
// valeur de l'écran des environnements : l'outil de déploiement dit l'intention,
// l'attestation dit le constat, et les deux peuvent diverger. Les fondre ferait
// disparaître exactement ce qu'on cherche à voir (B8).
//
// Une source qui n'atteste rien est un cas normal, pas une panne : le dashboard
// retombe alors sur ce que le déploiement déclare, et le dit à l'écran.
type Attester interface {
	// Attestations rend ce que le namespace rapporte de lui-même. Une erreur ici
	// n'est pas une anomalie du dashboard : la source peut être absente d'un
	// namespace qui n'a pas encore été déployé.
	Attestations(ctx context.Context, namespace string) ([]Attestation, error)
}

// Attestation est ce qu'une application en exécution rapporte d'elle-même.
type Attestation struct {
	Namespace string
	Name      string
	// Version est celle de l'**image** qui tourne, non celle de la release. Une
	// release finale réutilisant l'image de sa pré-release, le rapprochement se
	// fait sur `releases.image_version` et jamais sur le tag (B17, B8).
	Version string
	// State est l'état rapporté, repris tel quel pour l'affichage. Le vocabulaire
	// appartient à la source ; seul Ready porte un verdict.
	State string
	// Ready dit si l'application est exploitable — chez MSE, que ses migrations
	// ont abouti. C'est le seul cas où l'on peut affirmer qu'une version est
	// **livrée** : une image déployée dont les migrations ont échoué ne l'est
	// pas, et rien d'autre ne le signale (B8).
	Ready bool
}
