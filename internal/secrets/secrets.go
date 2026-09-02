// Package secrets chiffre les valeurs sensibles que les réglages de plugins
// gardent en base.
//
// # Pourquoi la base, alors que la règle disait le contraire
//
// La règle en vigueur — aucun identifiant dans PostgreSQL (doc 12 §12.7) — a été
// écrite pour les identifiants de registre, et elle reste juste **pour eux** :
// leur consommateur est Tekton, qui monte un `Secret` du cluster, et le
// dashboard n'a qu'à le désigner par son nom. Il n'a alors jamais besoin de le
// lire, ce que sa RBAC lui interdit d'ailleurs (doc 07 §7.3).
//
// La clé d'API d'un suivi a un autre consommateur : le dashboard lui-même. La
// désigner ne suffit pas, il faut la présenter à chaque appel. Trois voies
// existaient, et deux ont été écartées :
//
//   - la lire dans un `Secret` du cluster — refusée, elle demanderait `get` sur
//     les secrets, exactement le droit que la posture refuse ;
//   - la recevoir par l'environnement, une variable par projet — refusée, elle
//     ferait dépendre la déclaration d'un projet d'un redéploiement, ce que
//     §12.7 cherche justement à supprimer.
//
// Reste la base, chiffrée. C'est un abaissement réel du niveau de protection :
// une sauvegarde de la base devient un porteur de secrets, et la clé de
// chiffrement doit vivre ailleurs qu'elle. Les deux conséquences sont assumées.
//
// # Ce que ce paquet garantit, et ce qu'il ne garantit pas
//
// Il garantit qu'une valeur lue dans une sauvegarde de la base ne dit rien sans
// la clé, et qu'une clé remplacée ne rend pas les valeurs illisibles — l'ancienne
// continue de déchiffrer le temps de la rotation.
//
// Il ne garantit rien contre quelqu'un qui obtient à la fois la base et
// l'environnement du processus. Ce n'est pas un coffre : c'est ce qui fait qu'une
// fuite de base seule n'est pas une fuite de secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrNotConfigured signale qu'aucune clé n'a été fournie.
//
// Distinguée d'un échec de déchiffrement : « la clé manque » se corrige dans le
// déploiement, « la valeur est illisible » veut dire qu'on a changé de clé sans
// garder l'ancienne. Les confondre enverrait chercher au mauvais endroit.
var ErrNotConfigured = errors.New("aucune clé de chiffrement n'est configurée")

// ErrUnknownKey signale une valeur scellée par une clé que ce processus n'a pas.
//
// C'est le symptôme d'une rotation faite sans conserver la clé précédente. Le
// message nomme l'empreinte attendue : sans elle, retrouver laquelle des clés
// archivées convient serait une recherche à l'aveugle.
var ErrUnknownKey = errors.New("valeur scellée par une clé absente")

// envelopeVersion préfixe chaque valeur scellée.
//
// Présent dès la première version, alors qu'il n'y a rien à distinguer : le jour
// où la construction change — un autre AEAD, une dérivation de clé — les valeurs
// déjà en base doivent pouvoir dire de quelle construction elles viennent. Un
// format sans version se migre en devinant, et deviner sur du chiffré échoue en
// silence.
const envelopeVersion = "v1"

// keyLength est la taille exigée : AES-256, et rien d'autre.
//
// Accepter aussi AES-128 offrirait un choix dont personne ne veut : celui de se
// tromper. La taille étant vérifiée au démarrage, une clé mal produite se
// découvre au déploiement plutôt qu'à la première écriture.
const keyLength = 32

// Cipher scelle et ouvre les valeurs sensibles.
//
// Le zéro n'est pas utilisable : un `Cipher` non construit refuse de sceller
// plutôt que de rendre du clair. C'est la raison de [ErrNotConfigured] — sans
// elle, un déploiement sans clé écrirait des mots de passe lisibles dans la base
// sans que rien ne le signale.
type Cipher struct {
	// sealer est l'AEAD de la clé courante, celle qui scelle.
	sealer cipher.AEAD
	// sealerID est l'empreinte de cette clé, écrite dans l'enveloppe.
	sealerID string
	// openers porte toutes les clés connues, empreinte comprise : la courante et
	// celles qu'on garde le temps d'une rotation.
	openers map[string]cipher.AEAD
}

// New construit le chiffreur à partir de la clé courante et des clés retirées.
//
// Les clés sont en base64 standard, telles que `openssl rand -base64 32` les
// produit. Une clé vide parmi les précédentes est ignorée : c'est le cas courant
// d'une variable déclarée mais non renseignée, et le refuser obligerait le
// déploiement à supprimer la variable plutôt qu'à la vider.
func New(current string, retired ...string) (*Cipher, error) {
	block, id, err := aeadFor(current)
	if err != nil {
		return nil, fmt.Errorf("clé de chiffrement courante: %w", err)
	}

	c := &Cipher{sealer: block, sealerID: id, openers: map[string]cipher.AEAD{id: block}}
	for index, key := range retired {
		if strings.TrimSpace(key) == "" {
			continue
		}
		retiredBlock, retiredID, err := aeadFor(key)
		if err != nil {
			return nil, fmt.Errorf("clé de chiffrement retirée n°%d: %w", index+1, err)
		}
		// La courante l'emporte si elle est répétée : sceller avec elle reste le
		// comportement, et la lister deux fois n'est pas une erreur de
		// configuration qui mérite un refus de démarrer.
		if _, known := c.openers[retiredID]; !known {
			c.openers[retiredID] = retiredBlock
		}
	}
	return c, nil
}

// aeadFor décode une clé et en dérive son empreinte.
//
// L'empreinte est calculée sur la clé plutôt que sur son rang dans la
// configuration. Un rang change quand on retire une clé de la liste, et les
// valeurs déjà scellées désigneraient alors une autre clé — ce qui les rendrait
// illisibles au moment précis où la rotation croyait les préserver.
func aeadFor(key string) (cipher.AEAD, string, error) {
	material, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		// Le message ne cite pas la valeur : une clé mal collée reste une clé.
		return nil, "", fmt.Errorf("attendue en base64 (openssl rand -base64 32): %w", err)
	}
	if len(material) != keyLength {
		return nil, "", fmt.Errorf("longueur de %d octets, attendue %d", len(material), keyLength)
	}

	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}

	// Quatre octets de l'empreinte suffisent à désigner une clé parmi les
	// quelques-unes qu'un déploiement garde, et n'en révèlent rien : c'est un
	// condensat, et il faudrait la clé pour le reproduire.
	digest := sha256.Sum256(material)
	return aead, hex.EncodeToString(digest[:4]), nil
}

// Configured dit si une clé est disponible.
//
// Sert aux appelants qui doivent dégrader plutôt qu'échouer : un écran de
// réglages doit pouvoir afficher « le chiffrement n'est pas configuré » au lieu
// de proposer un champ qui refusera l'enregistrement.
func (c *Cipher) Configured() bool {
	return c != nil && c.sealer != nil
}

// Seal chiffre une valeur et rend son enveloppe.
//
// Une valeur vide ressort vide, sans chiffrement : c'est l'effacement d'un champ,
// et sceller le vide produirait une enveloppe indiscernable d'une valeur
// renseignée — l'écran dirait « clé enregistrée » sur un champ vidé.
func (c *Cipher) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if !c.Configured() {
		return "", ErrNotConfigured
	}

	nonce := make([]byte, c.sealer.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("tirage du nonce: %w", err)
	}

	// L'empreinte de la clé sert de données authentifiées : une enveloppe dont on
	// aurait maquillé le préfixe pour la faire ouvrir par une autre clé échoue à
	// l'authentification, au lieu de rendre des octets arbitraires.
	sealed := c.sealer.Seal(nonce, nonce, []byte(plaintext), []byte(c.sealerID))
	return envelopeVersion + ":" + c.sealerID + ":" +
		base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open déchiffre une enveloppe.
//
// Une chaîne vide ressort vide : c'est le champ jamais renseigné, et le traiter
// comme une erreur obligerait chaque appelant à le distinguer lui-même.
func (c *Cipher) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !c.Configured() {
		return "", ErrNotConfigured
	}

	version, keyID, payload, ok := splitEnvelope(sealed)
	if !ok {
		return "", errors.New("enveloppe malformée")
	}
	if version != envelopeVersion {
		return "", fmt.Errorf("enveloppe de version %q, ce binaire lit %q",
			version, envelopeVersion)
	}

	aead, known := c.openers[keyID]
	if !known {
		return "", fmt.Errorf("%w: empreinte %s", ErrUnknownKey, keyID)
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("enveloppe illisible: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("enveloppe tronquée")
	}

	nonce, body := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, body, []byte(keyID))
	if err != nil {
		// Le message ne dit pas pourquoi : distinguer « clé fausse » de « valeur
		// altérée » renseignerait qui essaie des clés.
		return "", errors.New("déchiffrement refusé")
	}
	return string(plaintext), nil
}

// NeedsResealing dit si une enveloppe a été scellée par une clé retirée.
//
// La rotation ne se fait pas d'un coup : les valeurs sont re-scellées quand elles
// passent par une écriture, et cette question dit lesquelles ne le sont pas
// encore. Sans elle, on ne saurait jamais quand la clé retirée peut disparaître
// de la configuration.
func (c *Cipher) NeedsResealing(sealed string) bool {
	if sealed == "" || !c.Configured() {
		return false
	}
	_, keyID, _, ok := splitEnvelope(sealed)
	return ok && keyID != c.sealerID
}

// splitEnvelope décompose `version:empreinte:charge`.
func splitEnvelope(sealed string) (version, keyID, payload string, ok bool) {
	parts := strings.SplitN(sealed, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
