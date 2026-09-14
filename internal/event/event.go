// Package event nomme les faits que le dashboard constate.
//
// # Un fait, jamais un ordre
//
// `release.created` dit qu'une release a été déclarée ; il ne dit pas quoi faire.
// La distinction n'est pas cosmétique : un fait est **rejouable** — on peut le
// relire, le réémettre, l'archiver — là qu'un ordre ne l'est pas, et c'est cette
// propriété qui permettra d'y accrocher des réactions sans que le noyau ait à les
// connaître (doc 14 §14.2).
//
// # Trois faits, et la règle qui les choisit
//
// **Un fait du noyau existe quand quelque chose arrive qu'aucun plan n'a causé**
// (doc 15 §15.3). Ce qu'un plan provoque lui appartient : il n'a pas à ressortir
// dans le fil des faits pour y rentrer aussitôt par une réaction.
//
// Trois faits ont été retirés sur cette règle — `release.built`,
// `release.verified`, `pipeline.finished`. Les deux premiers étaient des étapes
// du plan d'une release, et c'est désormais le **rôle de l'étape** qui fait
// avancer son état ; le troisième était le générique d'un run, et `when: failure`
// sur une étape couvre ce qu'on lui demandait.
//
// Restent trois points d'entrée : une version déclarée dans le suivi, une
// release déclarée, une recette validée. La dernière aurait pu tomber sous la
// même règle ; elle n'en relève pas, parce qu'elle est une **décision humaine**
// prise hors de toute automatisation.
//
// La promotion aboutie en est sortie sur un autre motif : c'est le plugin MSE qui
// définit ce qu'est une promotion, et c'est lui qui la constate, sous
// `mse.release.promoted` (doc 16 §16.7).
//
// Le type `Outcome` est parti avec eux : aucun des trois ne se termine d'une
// façon ou d'une autre. Un point d'entrée **arrive**, il ne conclut pas — ce qui
// conclut, ce sont les étapes d'un plan, et leur issue se lit dans l'exécution.
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
// L'observateur **redélivre** les runs terminés : resynchronisation de
// l'informer, redémarrage du worker. Les sondes font pire encore — celle du suivi
// relit l'intégralité des versions à chaque passe, parce que l'API de Redmine ne
// les filtre pas.
//
// Un fait sans clé stable serait donc réémis à chaque tour. Aujourd'hui personne
// ne les écoute et cela ne coûterait rien ; le jour où une réaction enverra une
// notification, cela en enverrait une par redémarrage du worker. La règle doit
// être posée maintenant, pendant qu'elle est gratuite à vérifier.
package event

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type nomme un fait.
//
// Les types du noyau sont nus — `release.created` — là où ceux d'un plugin seront
// préfixés par son identifiant : `gitea.pull_request.opened`. Deux plugins de
// forge coexisteront, et une réaction doit pouvoir dire lequel elle écoute.
type Type string

const (
	// VersionCreated : une version apparaît dans le suivi du projet.
	//
	// Du noyau bien qu'observé par un plugin, et c'est délibéré : tous les suivis
	// déposent leurs versions dans la même table, et une réaction déclarée sur
	// « une version existe » n'a pas à être réécrite quand le client change de
	// Redmine pour autre chose. Le plugin qui l'a constaté reste nommé par
	// [Event.Source].
	VersionCreated Type = "version.created"
	// ReleaseCreated : une release est déclarée. Le premier fait de sa vie.
	ReleaseCreated Type = "release.created"
	// ReleaseReleased : une release est validée pour livraison. La seule
	// transition que rien n'automatise.
	ReleaseReleased Type = "release.released"
)

// Event est un fait constaté.
type Event struct {
	Type Type
	// Source nomme **qui l'a constaté** : le plugin qui l'a observé, ou rien
	// quand le noyau l'a constaté lui-même.
	//
	// Ce n'est donc pas tout à fait la même chose que le préfixe du type, et
	// `version.created` est l'exception qui le montre : un fait du noyau, observé
	// par la sonde d'un plugin, qui porte son identifiant sans porter son préfixe.
	//
	// Explicite plutôt que déduit du nom : un plugin retiré d'une version laisse
	// des faits qu'on doit encore savoir attribuer, et les reconnaître à leur type
	// demanderait de connaître les plugins disparus.
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

// VersionCreatedAt rend le fait d'une version apparue dans le suivi.
//
// # La clé porte la version **locale**, et pas celle du suivi
//
// C'est ce qui rend le fait indépendant de l'outil qui l'a observé. Une instance
// qui migrerait de Redmine vers autre chose reconstaterait sinon tout son
// historique sous des identifiants neufs — et déclencherait une notification par
// version.
//
// Elle vaut aussi comme garde contre la forme de cette sonde : le suivi relit
// l'intégralité des versions à chaque passe, parce que l'API de Redmine ne les
// filtre pas. C'est l'unicité de la clé, et elle seule, qui fait qu'une version
// déjà vue n'est pas reconstatée — le magasin n'a pas à dire si son écriture a
// inséré ou mis à jour.
func VersionCreatedAt(
	at time.Time, versionID int64, name, status, tool, externalID string, dueDate *time.Time,
) Event {
	payload := map[string]string{
		"version_id": id(versionID), "name": name, "status": status,
		// L'outil et son identifiant : ce qui permet de remonter à la fiche, et de
		// distinguer deux suivis branchés sur le même projet.
		"tool": tool, "external_id": externalID,
	}
	// Une échéance absente reste absente. La rendre sous forme de chaîne vide
	// ferait qu'un appariement `due_date=` la reconnaîtrait, alors que l'absence
	// d'échéance n'est pas une échéance.
	if dueDate != nil {
		payload["due_date"] = dueDate.Format(time.DateOnly)
	}

	return Event{
		Type: VersionCreated, Subject: name,
		OccurredAt: at,
		DedupKey:   key(string(VersionCreated), id(versionID)),
		Payload:    payload,
	}
}

// ReleaseCreatedKey rend la clé du fait d'une release déclarée.
//
// Exportée pour que l'ouverture du plan et l'émission du fait composent la
// **même** clé : c'est elle qui rend l'exécution unique par release, et deux
// orthographes en ouvriraient deux (doc 15 §15.2).
func ReleaseCreatedKey(releaseID int64) string {
	return key(string(ReleaseCreated), id(releaseID))
}

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
		DedupKey:   ReleaseCreatedKey(releaseID),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag, "kind": kind,
			"git_ref": gitRef, "version": version,
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

func id(value int64) string { return fmt.Sprintf("%d", value) }
