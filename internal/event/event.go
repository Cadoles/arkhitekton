// Package event nomme les faits que le dashboard constate.
//
// # Un fait, jamais un ordre
//
// `release.built` dit qu'une construction s'est terminée ; il ne dit pas quoi
// faire. La distinction n'est pas cosmétique : un fait est **rejouable** — on peut
// le relire, le réémettre, l'archiver — là qu'un ordre ne l'est pas, et c'est
// cette propriété qui permettra d'y accrocher des réactions sans que le noyau ait
// à les connaître (doc 14 §14.2).
//
// # Ce paquet n'émet rien
//
// Il porte le vocabulaire et la **règle de déduplication**, rien d'autre. Les
// points d'émission vivent là où le fait se produit : l'`api` pour les gestes
// humains, le `worker` pour ce qu'il observe. Les faire passer par un émetteur
// commun n'apporterait qu'une indirection, alors que la seule chose qui doive
// vraiment être partagée est la façon de nommer un fait — deux façons finiraient
// par produire deux clés pour le même événement, et la déduplication ne servirait
// plus à rien.
//
// # Pourquoi la déduplication est le cœur du paquet
//
// L'observateur **redélivre** les runs terminés : resynchronisation de l'informer,
// redémarrage du worker. Le code s'en garde déjà explicitement, et le commentaire
// d'`advanceRelease` raconte le jour où cinq paires de tests sont parties pour une
// seule construction.
//
// Un fait sans clé stable serait donc réémis à chaque redélivrance. Aujourd'hui
// personne ne les écoute et cela ne coûterait rien ; le jour où une réaction
// enverra une notification, cela en enverrait une par redémarrage du worker. La
// règle doit être posée maintenant, pendant qu'elle est gratuite à vérifier.
package event

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type nomme un fait.
//
// Les types du noyau sont nus — `release.built` — là où ceux d'un plugin seront
// préfixés par son identifiant : `gitea.pull_request.opened`. Deux plugins de
// forge coexisteront, et une réaction doit pouvoir dire lequel elle écoute.
type Type string

const (
	// ReleaseCreated : une release est déclarée. Le premier fait de sa vie.
	ReleaseCreated Type = "release.created"
	// ReleaseBuilt : la construction d'une release se conclut, quelle qu'en soit
	// l'issue — `outcome` la porte.
	ReleaseBuilt Type = "release.built"
	// ReleaseVerified : toutes les vérifications d'une release ont rendu leur
	// verdict.
	ReleaseVerified Type = "release.verified"
	// ReleaseReleased : une release est validée pour livraison. La seule
	// transition que rien n'automatise.
	ReleaseReleased Type = "release.released"
	// ReleasePromoted : une promotion aboutit.
	//
	// **Le re-tag a réussi**, ce qui est un constat et non l'intention de le
	// demander. Ce que le déployeur en fait ensuite est une autre affaire, et
	// c'est l'écran des environnements qui la raconte (B8).
	ReleasePromoted Type = "release.promoted"
	// PipelineFinished : un run se termine, quel qu'il soit.
	//
	// Le générique dont les autres sont des commodités. Un projet qui veut
	// réagir à une famille précise le fait par une condition sur `kind`, plutôt
	// que par un type de plus.
	PipelineFinished Type = "pipeline.finished"
)

// Outcome dit comment un fait s'est terminé, quand la question se pose.
//
// Un champ de la charge et non un type d'événement : `release.built.failed`
// obligerait toute réaction à s'abonner deux fois pour suivre une construction,
// et le jour où une troisième issue apparaît, trois fois.
type Outcome string

const (
	Succeeded Outcome = "succeeded"
	Failed    Outcome = "failed"
	Cancelled Outcome = "cancelled"
)

// Event est un fait constaté.
type Event struct {
	Type Type
	// Source distingue le noyau d'un plugin. Vide pour le noyau.
	//
	// Explicite plutôt que déduit du préfixe du type : un plugin retiré d'une
	// version laisse des faits qu'on doit encore savoir attribuer, et les
	// reconnaître à leur nom demanderait de connaître les plugins disparus.
	Source string
	// ProjectID rattache le fait à son projet. Nil pour un fait qui n'en vise
	// aucun — un run lancé sans projet, antérieur à la migration 0035.
	ProjectID *int64
	// Subject nomme ce sur quoi porte le fait, dans le vocabulaire de l'écran :
	// un tag de release, un nom de run. Sert à le lire, jamais à le retrouver —
	// c'est le rôle de la charge.
	Subject string
	// Payload est une carte **plate** de chaînes, comme la configuration des
	// plugins et les réglages de déployeur.
	//
	// Plate parce qu'un appariement de réaction porte sur une valeur, pas sur un
	// chemin : `kind = final` se lit et s'édite, `$.release.kind` demanderait un
	// langage de chemins et l'interface qui va avec.
	Payload map[string]string
	// OccurredAt est l'instant du **fait**, jamais celui de sa lecture. La
	// distinction comptera pour les sondes, qui rattraperont des jours
	// d'historique : les dater à l'instant de la lecture ferait paraître que tout
	// s'est produit d'un coup.
	OccurredAt time.Time
	// Depth est la profondeur d'enchaînement, héritée du fait qui l'a causé.
	//
	// Zéro tant que rien ne réagit. Le champ existe dès maintenant parce qu'il
	// borne les boucles, et qu'un champ ajouté après coup laisse une histoire
	// dont la profondeur est inconnue.
	Depth int
	// DedupKey identifie le fait, pas sa lecture. Deux émissions du même fait
	// portent la même clé, et la seconde n'écrit rien.
	DedupKey string
}

// ErrInvalid signale un fait qu'on refuse d'enregistrer.
var ErrInvalid = errors.New("fait invalide")

// Validate refuse un fait qui ne pourrait pas être exploité.
//
// La clé de déduplication est le contrôle qui compte : sans elle, l'insertion
// réussirait et le fait serait réémis à chaque redélivrance. Le défaut ne se
// verrait qu'une fois une réaction branchée, très loin d'ici.
func (e Event) Validate() error {
	var problems []error

	if strings.TrimSpace(string(e.Type)) == "" {
		problems = append(problems, fmt.Errorf("%w: type absent", ErrInvalid))
	}
	if strings.TrimSpace(e.DedupKey) == "" {
		problems = append(problems, fmt.Errorf(
			"%w: clé de déduplication absente pour %q — le fait serait réémis à "+
				"chaque redélivrance", ErrInvalid, e.Type))
	}
	if e.OccurredAt.IsZero() {
		problems = append(problems, fmt.Errorf("%w: instant absent pour %q", ErrInvalid, e.Type))
	}
	if e.Depth < 0 {
		problems = append(problems, fmt.Errorf("%w: profondeur négative (%d)", ErrInvalid, e.Depth))
	}

	return errors.Join(problems...)
}

// key compose une clé de déduplication.
//
// Rendue ici et jamais chez l'appelant : c'est la seule chose que les deux
// émetteurs — l'`api` et le `worker` — doivent absolument partager, et deux
// façons de la composer produiraient deux clés pour le même fait.
func key(parts ...string) string {
	return strings.Join(parts, ":")
}

/*
 * Les constructeurs.
 *
 * Un par fait, plutôt qu'une fabrique générique : c'est ici que vit la règle de
 * déduplication, et l'écrire une fois par fait est ce qui rend lisible **ce qui
 * identifie ce fait-là**. Une fabrique unique aurait renvoyé cette décision à
 * chaque appelant, c'est-à-dire à l'endroit où elle se trompe.
 */

// ReleaseCreatedAt rend le fait d'une release déclarée.
//
// Clé sur la release seule : une release n'est créée qu'une fois, et un second
// appel serait un doublon que la contrainte d'unicité refuse déjà en amont.
func ReleaseCreatedAt(
	at time.Time, projectID, releaseID int64, tag, kind, gitRef, version string,
) Event {
	return Event{
		Type: ReleaseCreated, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   key(string(ReleaseCreated), id(releaseID)),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag, "kind": kind,
			"git_ref": gitRef, "version": version,
		},
	}
}

// ReleaseBuiltKey rend la clé du fait d'une construction conclue.
//
// Exportée parce qu'un second appelant en a besoin : le moteur de plans ouvre son
// exécution sur **ce fait-là**, et deux façons de le nommer produiraient une
// exécution par redélivrance. La clé du fait et celle de l'exécution qu'il
// déclenche doivent être la même chaîne, composée au même endroit.
func ReleaseBuiltKey(releaseID, jobID int64) string {
	return key(string(ReleaseBuilt), id(releaseID), id(jobID))
}

// ReleaseBuiltAt rend le fait d'une construction conclue.
//
// Clé sur la release **et le job** : une release se reconstruit — c'est le geste
// qui répare un échec d'infrastructure sans faire porter au numéro de version
// l'historique des pannes du cluster —, et chaque construction est un fait
// distinct. Une clé sur la seule release ferait taire toutes les suivantes.
func ReleaseBuiltAt(
	at time.Time, projectID, releaseID, jobID int64, tag string, outcome Outcome, commit string,
) Event {
	return Event{
		Type: ReleaseBuilt, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   ReleaseBuiltKey(releaseID, jobID),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag,
			"outcome": string(outcome), "job_id": id(jobID), "git_commit": commit,
		},
	}
}

// ReleaseVerifiedAt rend le fait d'une release dont les vérifications ont conclu.
//
// Clé sur la release et le job **qui a conclu** — la dernière vérification à
// rendre son verdict. Une release relancée conclut à nouveau, et la clé change
// avec le job : sans cela, la seconde conclusion serait silencieuse et une
// réaction ne verrait jamais qu'une reprise a réussi.
func ReleaseVerifiedAt(
	at time.Time, projectID, releaseID, jobID int64, tag string, outcome Outcome, kinds []string,
) Event {
	return Event{
		Type: ReleaseVerified, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   key(string(ReleaseVerified), id(releaseID), id(jobID)),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag,
			"outcome": string(outcome), "kinds": strings.Join(kinds, ","),
		},
	}
}

// ReleaseReleasedAt rend le fait d'une release validée pour livraison.
func ReleaseReleasedAt(at time.Time, projectID, releaseID int64, tag, by string) Event {
	return Event{
		Type: ReleaseReleased, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   key(string(ReleaseReleased), id(releaseID)),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag, "released_by": by,
		},
	}
}

// ReleasePromotedAt rend le fait d'une promotion aboutie.
//
// Clé sur le job : une même release se promeut plusieurs fois vers le même
// environnement — une reprise après un re-tag manqué —, et chaque aboutissement
// est un fait.
func ReleasePromotedAt(
	at time.Time, projectID, releaseID, jobID int64, tag, environment string,
) Event {
	return Event{
		Type: ReleasePromoted, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   key(string(ReleasePromoted), id(jobID)),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag,
			"environment": environment, "job_id": id(jobID),
		},
	}
}

// PipelineFinishedAt rend le fait d'un run terminé.
//
// Clé sur le **run** et non sur le job : un job en échec est remis en file, et
// ses tentatives sont autant de runs distincts. Une clé sur le job ferait
// disparaître toutes les tentatives après la première.
func PipelineFinishedAt(
	at time.Time, projectID *int64, runID, jobID int64,
	runName, kind string, outcome Outcome, releaseID *int64,
) Event {
	payload := map[string]string{
		"run": runName, "run_id": id(runID), "kind": kind,
		"outcome": string(outcome), "job_id": id(jobID),
	}
	if releaseID != nil {
		payload["release_id"] = id(*releaseID)
	}

	return Event{
		Type: PipelineFinished, ProjectID: projectID, Subject: runName,
		OccurredAt: at,
		DedupKey:   key(string(PipelineFinished), id(runID)),
		Payload:    payload,
	}
}

func id(value int64) string { return fmt.Sprintf("%d", value) }
