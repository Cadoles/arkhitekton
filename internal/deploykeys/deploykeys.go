// Package deploykeys donne à chaque projet sa propre clé de déploiement.
//
// Une seule clé ouvrait jusqu'ici tous les dépôts déclarés. À l'échelle d'un
// produit, cela ne tient pas : une clé compromise se révoque projet par projet,
// et un projet nouvellement déclaré ne devrait pas recevoir l'accès à ceux qui
// l'ont précédé (doc 12 §12.7, lot G).
//
// # Ce que le dashboard fait, et ce qu'il ne fait pas
//
// Il **génère** la paire dans le cluster, écrit la moitié privée dans un Secret
// que les pipelines montent, et ne conserve que la moitié publique — celle qu'on
// colle dans les réglages d'un dépôt. Il ne relit jamais ce qu'il a écrit.
//
// Cela honore l'invariant de [07 §7.3] à la lettre : « le dashboard désigne, il
// ne distribue pas ». La matière du secret ne transite ni par une spec, ni par un
// paramètre, ni par un journal, ni par une réponse d'API. Elle passe une fois, du
// générateur au Secret, et n'en ressort plus.
//
// # Pourquoi il ne peut pas lire, et ce que cela impose
//
// La RBAC accorde `create`, `patch` et `delete` sur les Secrets, **sans** `get`
// ni `list`. En RBAC Kubernetes les verbes sont indépendants, et c'est ce qui
// permet à un rôle de posséder ce qu'il crée sans rien pouvoir lire du namespace
// — lequel contient les identifiants de registre, de MinIO et la clé partagée.
//
// Conséquence directe sur la conception : **on ne peut pas interroger le cluster
// pour savoir si un Secret nous appartient**. La propriété se lit donc ailleurs,
// dans la base : un projet dont `deploy_key_public` est renseigné a reçu sa clé
// de nous. Si le Secret existe alors que la base ne connaît aucune clé, c'est une
// collision de noms avec quelque chose que nous n'avons pas créé — et le
// dashboard **refuse** plutôt que d'adopter (doc 12 §12.7, « il n'adopte jamais
// un secret préexistant »).
package deploykeys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ManagedBy marque les ressources que le dashboard possède.
//
// Elle ne sert pas à décider quoi que ce soit — sans `get`, on ne la relit
// jamais — mais à ce qu'un humain devant `kubectl get secret` sache d'où vient
// l'objet et qui le supprimera.
const ManagedBy = "arkhitekton"

// PrivateKeyFile est le nom sous lequel la clé est déposée dans le Secret.
//
// # Ce nom-là, et pas un autre
//
// Le workspace `ssh-directory` de la Task `git-clone` est **recopié tel quel dans
// `~/.ssh`**. OpenSSH n'y charge que les identités qu'il connaît — `id_ed25519`,
// `id_rsa` — ou celles qu'un `config` désigne. Un fichier nommé autrement est
// présent et ignoré.
//
// C'est le défaut qui a fait échouer toutes les constructions après le passage
// aux clés par projet : le Secret ne portait que `ssh-privatekey`, le nom imposé
// par le **type** `kubernetes.io/ssh-auth`. Ce nom est juste pour le type, et
// muet pour SSH. La clé partagée qu'il remplaçait portait `id_ed25519`, et c'est
// pour cela qu'elle fonctionnait.
//
// Symptôme, pour qui le reverrait : `git-init` prévient « appears to need SSH
// authentication but no SSH credentials have been provided », puis la forge
// répond `Permission denied (publickey)` — alors même que la clé publique est
// bien déclarée sur le dépôt.
const PrivateKeyFile = "id_ed25519"

// secretData compose le contenu du Secret.
//
// La clé est déposée **deux fois**, sous les deux noms. Ce n'est pas une
// hésitation :
//
//   - `ssh-privatekey` est exigé par le type `kubernetes.io/ssh-auth`, qu'on
//     garde pour que l'objet reste typé et reconnaissable ;
//   - [PrivateKeyFile] est celui que SSH lit une fois le workspace recopié.
//
// Les deux désignent la même matière, donc la même portée : rien n'est exposé de
// plus qu'avec un seul fichier.
func secretData(private []byte) map[string][]byte {
	return map[string][]byte{
		corev1.SSHAuthPrivateKey: private,
		PrivateKeyFile:           private,
	}
}

// ErrNameTaken signale qu'un Secret porte déjà le nom que le projet réclame.
//
// Ce n'est pas une erreur d'écriture : c'est le refus d'adopter. Le Secret
// existe, la base ne connaît aucune clé pour ce projet, donc il vient d'ailleurs.
// L'écraser détruirait la clé de quelqu'un d'autre, et l'adopter reviendrait à
// affirmer une propriété qu'on ne peut pas vérifier.
var ErrNameTaken = errors.New("un secret porte déjà ce nom et n'a pas été créé par le dashboard")

// SecretName rend le nom du Secret portant la clé d'un projet.
//
// Dérivé du slug, qui est déjà contraint à `^[a-z0-9][a-z0-9-]*$` par la
// migration 0026 — donc un nom de ressource Kubernetes valide sans échappement.
// Le préfixe rend la propriété lisible sans consulter les étiquettes.
func SecretName(slug string) string {
	return "arkhitekton-project-" + slug + "-git"
}

// Pair est une paire de clés fraîchement engendrée.
type Pair struct {
	// Private est la clé privée au format OpenSSH. Elle ne quitte cette
	// structure que pour entrer dans un Secret.
	Private []byte
	// Public est la ligne `authorized_keys`. C'est la seule moitié que le
	// dashboard conserve, et la seule qu'il affiche.
	Public string
}

// Generate engendre une paire ed25519.
//
// ed25519 et non RSA : les clés sont courtes, la génération est immédiate, et
// toutes les forges que ce produit vise l'acceptent depuis des années. Une clé
// RSA de taille suffisante prendrait un temps sensible dans une requête HTTP,
// pour un gain nul.
func Generate(comment string) (Pair, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, fmt.Errorf("génération de la paire: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(private, comment)
	if err != nil {
		return Pair{}, fmt.Errorf("encodage de la clé privée: %w", err)
	}

	signer, err := ssh.NewPublicKey(public)
	if err != nil {
		return Pair{}, fmt.Errorf("encodage de la clé publique: %w", err)
	}

	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer)))
	if comment != "" {
		line += " " + comment
	}

	return Pair{Private: pem.EncodeToMemory(block), Public: line}, nil
}

// Owner crée et supprime les Secrets de clé, sans jamais en lire aucun.
type Owner struct {
	kube      kubernetes.Interface
	namespace string
}

func NewOwner(kube kubernetes.Interface, namespace string) *Owner {
	return &Owner{kube: kube, namespace: namespace}
}

// Create dépose la moitié privée dans un Secret et rend la moitié publique.
//
// `known` dit si la base connaît déjà une clé pour ce projet. C'est ce paramètre
// qui remplace la lecture du cluster : sans lui, on ne saurait pas distinguer
// « notre Secret, à remplacer » de « le Secret d'un autre, à ne pas toucher ».
//
// Quand la clé est connue, le Secret est **remplacé** : c'est une rotation, geste
// délibéré dont l'ancienne moitié publique devient caduque côté forge. Quand elle
// ne l'est pas, la création est la seule opération tentée, et un conflit rend
// [ErrNameTaken].
func (o *Owner) Create(ctx context.Context, slug, comment string, known bool) (string, error) {
	pair, err := Generate(comment)
	if err != nil {
		return "", err
	}

	name := SecretName(slug)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: o.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": ManagedBy,
				"arkhitekton/project":          slug,
			},
		},
		Type: corev1.SecretTypeSSHAuth,
		Data: secretData(pair.Private),
	}

	secrets := o.kube.CoreV1().Secrets(o.namespace)

	_, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
	switch {
	case err == nil:
		return pair.Public, nil

	case apierrors.IsAlreadyExists(err) && !known:
		// Le refus d'adopter. Sans `get`, on ne peut pas vérifier que ce Secret
		// est le nôtre, et la base dit qu'il ne l'est pas.
		return "", fmt.Errorf("%w: %s", ErrNameTaken, name)

	case apierrors.IsAlreadyExists(err):
		// Rotation : la base atteste que cette clé est la nôtre. `Update`
		// remplace la matière sans qu'on ait eu à la lire.
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("rotation de la clé du projet %q: %w", slug, err)
		}
		return pair.Public, nil

	default:
		return "", fmt.Errorf("création de la clé du projet %q: %w", slug, err)
	}
}

// Delete retire le Secret d'un projet.
//
// L'absence n'est pas une erreur : supprimer un projet dont la clé a déjà
// disparu doit aboutir, sans quoi une suppression resterait bloquée sur un état
// qu'on ne peut de toute façon pas constater.
func (o *Owner) Delete(ctx context.Context, slug string) error {
	err := o.kube.CoreV1().Secrets(o.namespace).
		Delete(ctx, SecretName(slug), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("suppression de la clé du projet %q: %w", slug, err)
	}
	return nil
}

/*
 * La liste des secrets montables.
 *
 * `kubernetes.io/enforce-mountable-secrets: "true"` sur le compte de service est
 * ce qui empêche réellement une Task de se servir dans le namespace — pas la
 * RBAC du dashboard, qui ne régit que ses propres appels. Le dashboard crée déjà
 * des PipelineRun, donc indirectement des pods : sans cette annotation, il
 * pourrait déjà faire monter n'importe quel secret montable (doc 12 §12.7).
 *
 * En devenant propriétaire de secrets, il devient responsable de ce qui peut les
 * monter. C'est une charge nouvelle, et le prix de la posture d'opérateur.
 *
 * Deux règles la bornent :
 *
 *   1. Il n'ajoute que des noms qu'il **possède** — ceux que [SecretName]
 *      produit. Un nom quelconque passé ici ouvrirait au montage un secret dont
 *      il ne sait rien.
 *   2. Il ne **retire** jamais une entrée qu'il n'a pas posée. Le compte de
 *      service porte aussi les identifiants du déploiement, et les élaguer
 *      casserait les pipelines qui les emploient.
 */

// Mounter tient à jour la liste des secrets montables des comptes de service.
//
// **Plusieurs**, et c'est nécessaire : le catalogue nomme un compte par
// pipeline — `tekton-kubectl` pour celui qui déploie, le compte par défaut pour
// les autres. N'en tenir qu'un rendrait la clé d'un projet montable pour
// certains de ses pipelines et pas pour les autres, et l'échec désignerait le
// clone plutôt que le montage.
type Mounter struct {
	kube      kubernetes.Interface
	namespace string
	accounts  []string
}

func NewMounter(kube kubernetes.Interface, namespace string, accounts ...string) *Mounter {
	return &Mounter{kube: kube, namespace: namespace, accounts: accounts}
}

// Allow rend montable la clé d'un projet.
//
// Idempotent : réappeler après un redémarrage ne doit pas dupliquer l'entrée, et
// la liste est comparée avant d'écrire pour ne pas produire un `patch` par
// démarrage sur un compte de service qui n'a pas bougé.
func (m *Mounter) Allow(ctx context.Context, slug string) error {
	name := SecretName(slug)

	var problems []error
	for _, target := range m.accounts {
		problems = append(problems, m.allowOn(ctx, target, name))
	}
	return errors.Join(problems...)
}

func (m *Mounter) allowOn(ctx context.Context, target, name string) error {
	accounts := m.kube.CoreV1().ServiceAccounts(m.namespace)
	account, err := accounts.Get(ctx, target, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("lecture du compte de service %q: %w", target, err)
	}

	for _, reference := range account.Secrets {
		if reference.Name == name {
			return nil
		}
	}

	account.Secrets = append(account.Secrets, corev1.ObjectReference{Name: name})
	if _, err := accounts.Update(ctx, account, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("autorisation du montage de %q sur %q: %w", name, target, err)
	}
	return nil
}

// Deny retire la clé d'un projet de la liste.
//
// Ne retire que ce que [SecretName] produit : les identifiants du déploiement
// restent en place. La garantie est **structurelle** et non vérifiée à
// l'exécution — ces deux méthodes prennent un slug, jamais un nom de secret, et
// dérivent le nom elles-mêmes. Un appelant ne peut donc pas désigner une entrée
// qui ne nous appartient pas, ce qui est plus solide qu'un contrôle qu'on
// pourrait oublier d'écrire.
func (m *Mounter) Deny(ctx context.Context, slug string) error {
	name := SecretName(slug)

	var problems []error
	for _, target := range m.accounts {
		problems = append(problems, m.denyOn(ctx, target, name))
	}
	return errors.Join(problems...)
}

func (m *Mounter) denyOn(ctx context.Context, target, name string) error {
	accounts := m.kube.CoreV1().ServiceAccounts(m.namespace)
	account, err := accounts.Get(ctx, target, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("lecture du compte de service %q: %w", target, err)
	}

	kept := make([]corev1.ObjectReference, 0, len(account.Secrets))
	removed := false
	for _, reference := range account.Secrets {
		if reference.Name == name {
			removed = true
			continue
		}
		kept = append(kept, reference)
	}
	if !removed {
		return nil
	}

	account.Secrets = kept
	if _, err := accounts.Update(ctx, account, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("retrait du montage de %q sur %q: %w", name, target, err)
	}
	return nil
}

// Issuer réunit les deux gestes que la pose d'une clé demande.
//
// Créer le Secret ne suffit pas : sans entrée dans la liste montable du compte
// de service, le pod est refusé à la création, avec un message qui désigne le
// clone et non le montage. C'est ce couplage qui justifie un type plutôt que
// deux appels laissés à l'appelant — l'un sans l'autre produit une panne dont la
// cause est invisible.
type Issuer struct {
	owner   *Owner
	mounter *Mounter
}

func NewIssuer(owner *Owner, mounter *Mounter) *Issuer {
	return &Issuer{owner: owner, mounter: mounter}
}

// Issue dépose la clé et l'autorise au montage.
//
// L'ordre compte : le Secret d'abord. Autoriser au montage un secret qui
// n'existe pas laisserait une entrée pendante dans le compte de service, que
// personne n'irait retirer.
func (i *Issuer) Issue(ctx context.Context, slug string, known bool) (string, error) {
	public, err := i.owner.Create(ctx, slug, slug, known)
	if err != nil {
		return "", err
	}
	if err := i.mounter.Allow(ctx, slug); err != nil {
		return "", err
	}
	return public, nil
}

// Revoke retire l'autorisation puis la clé.
//
// L'ordre inverse de la pose, et pour la même raison : retirer d'abord le Secret
// laisserait une entrée pendante si le second geste échouait.
//
// Les deux erreurs sont réunies plutôt que la première rendue : un échec sur
// l'une n'empêche pas de tenter l'autre, et laisser le Secret en place parce que
// le compte de service n'a pas répondu serait le pire des deux mondes.
func (i *Issuer) Revoke(ctx context.Context, slug string) error {
	return errors.Join(i.mounter.Deny(ctx, slug), i.owner.Delete(ctx, slug))
}
