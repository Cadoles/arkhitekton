package flux

/*
 * Ce que le déployeur échange avec le plugin.
 *
 * Ces types formaient la frontière `provider.Deployer`, écrite pour qu'un autre
 * outil de déploiement puisse remplacer Flux. Ils n'avaient qu'une réalisation, et
 * leur seul appelant est le plugin MSE : ils vivent désormais à côté du déployeur
 * qui les emploie (doc 16 §16.5).
 */

// Target est un environnement, vu du déployeur.
type Target struct {
	ID string
	// Observable dit si l'on peut interroger cet environnement. Faux pour la
	// production, dans son cluster indépendant. C'est un état distinct de
	// « inconnu » : sans la distinction, une carte figée ressemble à une panne.
	Observable bool
	// Settings porte les réglages déclarés au catalogue pour cet environnement :
	// [SettingSuffix], [SettingAcceptsPrerelease], [SettingKustomization].
	Settings map[string]string
}

// Setting rend un réglage, ou la valeur par défaut fournie.
func (t Target) Setting(key, fallback string) string {
	if value, found := t.Settings[key]; found && value != "" {
		return value
	}
	return fallback
}

// Deployment est une demande de promotion.
type Deployment struct {
	Target Target
	// Version est l'identité de la release — son tag. C'est elle qui décide de
	// l'éligibilité à un environnement.
	Version string
	// Artifact est ce sous quoi l'image existe réellement. Distinct de Version, et
	// l'écart est le piège central : une release finale produite par re-tag porte
	// le tag `1.8.0` alors que son image existe sous `1.8.0-rc3` (B17).
	Artifact string
	// Name est le dépôt d'image court à promouvoir. Une release en publie
	// plusieurs, et le déployeur est interrogé une fois par image.
	Name string
}

// Plan décrit l'acheminement à réaliser : un pipeline et ses paramètres.
type Plan struct {
	// Kind est la famille de pipeline qui réalise l'acheminement.
	Kind string
	// Params sont ses paramètres.
	Params map[string]string
	// Reference est le tag posé sur l'image, pour l'historique des promotions.
	Reference string
}

// Resolution dit d'où vient ce qu'on affirme d'un environnement.
//
// Attester qu'une version tourne n'est pas la même affirmation que constater le
// tag que Flux a retenu. Les confondre ferait annoncer « livré » un environnement
// dont les migrations ont échoué (B8). Les valeurs sont celles écrites en base et
// rendues par l'interface.
type Resolution string

const (
	// ResolutionUnobservable : on ne peut rien affirmer.
	ResolutionUnobservable Resolution = "unobservable"
	// ResolutionDeclared : Flux dit ce qu'il a retenu. Une intention, pas un
	// constat — la valeur historique est `git`.
	ResolutionDeclared Resolution = "git"
	// ResolutionAttested : l'application rapporte sa version, et que ses
	// migrations ont abouti. Seul niveau qui autorise « livré ».
	ResolutionAttested Resolution = "portaldata"
)

// Observation est ce que Flux sait d'un environnement.
type Observation struct {
	// Version est ce que Flux a retenu. Vide quand il ne sait pas.
	Version string
	// State est l'état rapporté, repris tel quel pour l'affichage.
	State string
	// Resolution dit ce que vaut ce qui précède.
	Resolution Resolution
}
