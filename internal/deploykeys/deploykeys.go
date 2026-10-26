// Package deploykeys gives each project its own deploy key.
//
// A single key used to open every declared repository. At the scale of a product that does not
// hold: a compromised key is revoked project by project, and a newly declared project should not
// receive access to those that preceded it.
//
// # What the dashboard does, and what it does not
//
// It **generates** the pair in the cluster, writes the private half into a Secret the pipelines
// mount, and keeps only the public half — the one pasted into a repository's settings. It never
// reads back what it wrote.
//
// That honours the invariant to the letter: the dashboard designates, it does not distribute.
// The secret material passes through no spec, no parameter, no log, no API response. It passes
// once, from the generator to the Secret, and does not come out again.
//
// # Why it cannot read, and what that imposes
//
// The RBAC grants `create`, `patch` and `delete` on Secrets, **without** `get` or `list`. In
// Kubernetes RBAC the verbs are independent, and that is what lets a role own what it creates
// without being able to read anything of the namespace — which contains the registry and MinIO
// credentials and the shared key.
//
// A direct consequence on the design: **the cluster cannot be queried to know whether a Secret
// belongs to us**. Ownership is therefore read elsewhere, in the database: a project whose
// `deploy_key_public` is set received its key from us. If the Secret exists while the database
// knows of no key, it is a name collision with something we did not create — and the dashboard
// **refuses** rather than adopt: it never adopts a pre-existing secret.
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

// ManagedBy marks the resources the dashboard owns.
//
// It does not serve to decide anything — without `get`, it is never read back — but so that a
// human in front of `kubectl get secret` knows where the object comes from and who will delete
// it.
const ManagedBy = "arkhitekton"

// PrivateKeyFile is the name under which the key is deposited in the Secret.
//
// # That name, and no other
//
// The `git-clone` Task's `ssh-directory` workspace is **copied as it is into `~/.ssh`**. OpenSSH
// loads there only the identities it knows — `id_ed25519`, `id_rsa` — or those a `config` names.
// A file named otherwise is present and ignored.
//
// It is the defect that failed every build after the move to per-project keys: the Secret
// carried only `ssh-privatekey`, the name the `kubernetes.io/ssh-auth` **type** imposes. That
// name is right for the type, and mute for SSH. The shared key it replaced carried `id_ed25519`,
// and that is why it worked.
//
// The symptom, for whoever sees it again: `git-init` warns "appears to need SSH authentication
// but no SSH credentials have been provided", then the forge answers `Permission denied
// (publickey)` — even though the public key is indeed declared on the repository.
const PrivateKeyFile = "id_ed25519"

// secretData composes the Secret's content.
//
// The key is deposited **twice**, under both names. It is no hesitation:
//
//   - `ssh-privatekey` is required by the `kubernetes.io/ssh-auth` type, which is kept so that
//     the object stays typed and recognisable;
//   - [PrivateKeyFile] is the one SSH reads once the workspace is copied.
//
// Both name the same material, therefore the same scope: nothing is exposed beyond what a single
// file would expose.
func secretData(private []byte) map[string][]byte {
	return map[string][]byte{
		corev1.SSHAuthPrivateKey: private,
		PrivateKeyFile:           private,
	}
}

// ErrNameTaken reports that a Secret already carries the name the project claims.
//
// It is not a write error: it is the refusal to adopt. The Secret exists, the database knows of
// no key for this project, so it comes from elsewhere. Overwriting it would destroy someone
// else's key, and adopting it would amount to asserting an ownership that cannot be checked.
var ErrNameTaken = errors.New("a secret already carries this name and was not created by the dashboard")

// SecretName returns the name of the Secret carrying a project's key.
//
// Derived from the slug, which is already constrained to `^[a-z0-9][a-z0-9-]*$` by a migration —
// hence a valid Kubernetes resource name with no escaping. The prefix makes ownership readable
// without consulting the labels.
func SecretName(slug string) string {
	return "arkhitekton-project-" + slug + "-git"
}

// Pair is a freshly generated key pair.
type Pair struct {
	// Private is the private key in OpenSSH format. It leaves this structure only to enter a Secret.
	Private []byte
	// Public is the `authorized_keys` line. It is the only half the dashboard keeps, and the only
	// one it displays.
	Public string
}

// Generate generates an ed25519 pair.
//
// ed25519 and not RSA: the keys are short, generation is immediate, and every forge this product
// targets has accepted it for years. An RSA key of sufficient size would take a noticeable time
// inside an HTTP request, for no gain.
func Generate(comment string) (Pair, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, fmt.Errorf("generating the pair: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(private, comment)
	if err != nil {
		return Pair{}, fmt.Errorf("encoding the private key: %w", err)
	}

	signer, err := ssh.NewPublicKey(public)
	if err != nil {
		return Pair{}, fmt.Errorf("encoding the public key: %w", err)
	}

	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer)))
	if comment != "" {
		line += " " + comment
	}

	return Pair{Private: pem.EncodeToMemory(block), Public: line}, nil
}

// Owner creates and deletes the key Secrets, without ever reading any of them.
type Owner struct {
	kube      kubernetes.Interface
	namespace string
}

func NewOwner(kube kubernetes.Interface, namespace string) *Owner {
	return &Owner{kube: kube, namespace: namespace}
}

// Create deposits the private half into a Secret and returns the public half.
//
// `known` says whether the database already knows a key for this project. It is that parameter
// which replaces reading the cluster: without it, "our Secret, to be replaced" could not be told
// from "someone else's Secret, not to be touched".
//
// When the key is known, the Secret is **replaced**: it is a rotation, a deliberate act whose
// old public half becomes void on the forge's side. When it is not, creation is the only
// operation attempted, and a conflict returns [ErrNameTaken].
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
		// The refusal to adopt. Without `get`, it cannot be checked that this Secret is ours, and the
		// database says it is not.
		return "", fmt.Errorf("%w: %s", ErrNameTaken, name)

	case apierrors.IsAlreadyExists(err):
		// Rotation: the database attests that this key is ours. `Update` replaces the material without
		// our having had to read it.
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("rotating the key of project %q: %w", slug, err)
		}
		return pair.Public, nil

	default:
		return "", fmt.Errorf("creating the key of project %q: %w", slug, err)
	}
}

// Delete removes a project's Secret.
//
// Absence is not an error: deleting a project whose key has already disappeared must complete,
// otherwise a deletion would stay stuck on a state that cannot be observed anyway.
func (o *Owner) Delete(ctx context.Context, slug string) error {
	err := o.kube.CoreV1().Secrets(o.namespace).
		Delete(ctx, SecretName(slug), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting the key of project %q: %w", slug, err)
	}
	return nil
}

/*
 * The list of mountable secrets.
 *
 * `kubernetes.io/enforce-mountable-secrets: "true"` on the service account is what actually stops
 * a Task helping itself in the namespace — not the dashboard's RBAC, which governs only its own
 * calls. The dashboard already creates PipelineRuns, hence indirectly pods: without that
 * annotation, it could already have any mountable secret mounted.
 *
 * By becoming the owner of secrets, it becomes responsible for what can mount them. It is a new
 * burden, and the price of the operator posture.
 *
 * Two rules bound it:
 *
 *   1. It adds only names it **owns** — those [SecretName] produces. An arbitrary name passed
 *      here would open for mounting a secret it knows nothing about.
 *   2. It **never** removes an entry it did not set. The service account also carries the
 *      deployment's credentials, and pruning them would break the pipelines that use them.
 */

// Mounter keeps up to date the list of the service accounts' mountable secrets.
//
// **Several**, and it is necessary: the catalogue names one account per pipeline —
// `tekton-kubectl` for the one that deploys, the default account for the others. Keeping only
// one would make a project's key mountable for some of its pipelines and not for the others, and
// the failure would name the clone rather than the mount.
type Mounter struct {
	kube      kubernetes.Interface
	namespace string
	accounts  []string
}

func NewMounter(kube kubernetes.Interface, namespace string, accounts ...string) *Mounter {
	return &Mounter{kube: kube, namespace: namespace, accounts: accounts}
}

// Allow makes a project's key mountable.
//
// Idempotent: calling it again after a restart must not duplicate the entry, and the list is
// compared before writing so as not to produce one `patch` per start-up on a service account
// that has not moved.
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
		return fmt.Errorf("reading the service account %q: %w", target, err)
	}

	for _, reference := range account.Secrets {
		if reference.Name == name {
			return nil
		}
	}

	account.Secrets = append(account.Secrets, corev1.ObjectReference{Name: name})
	if _, err := accounts.Update(ctx, account, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("allowing %q to be mounted on %q: %w", name, target, err)
	}
	return nil
}

// Deny removes a project's key from the list.
//
// Removes only what [SecretName] produces: the deployment's credentials stay in place. The
// guarantee is **structural** and not checked at run time — these two methods take a slug, never
// a secret name, and derive the name themselves. A caller therefore cannot name an entry that is
// not ours, which is sturdier than a check one might forget to write.
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
		return fmt.Errorf("reading the service account %q: %w", target, err)
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
		return fmt.Errorf("removing the mount of %q on %q: %w", name, target, err)
	}
	return nil
}

// Issuer brings together the two acts that setting a key requires.
//
// Creating the Secret is not enough: with no entry in the service account's mountable list, the
// pod is refused at creation, with a message that names the clone and not the mount. It is that
// coupling which justifies a type rather than two calls left to the caller — one without the
// other produces an outage whose cause is invisible.
type Issuer struct {
	owner   *Owner
	mounter *Mounter
}

func NewIssuer(owner *Owner, mounter *Mounter) *Issuer {
	return &Issuer{owner: owner, mounter: mounter}
}

// Issue deposits the key and allows it to be mounted.
//
// The order counts: the Secret first. Allowing the mounting of a secret that does not exist would
// leave a dangling entry in the service account, which nobody would go and remove.
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

// Revoke removes the authorization then the key.
//
// The reverse order of setting, and for the same reason: removing the Secret first would leave a
// dangling entry if the second act failed.
//
// Both errors are joined rather than the first returned: a failure on one does not prevent
// attempting the other, and leaving the Secret in place because the service account did not
// answer would be the worst of both worlds.
func (i *Issuer) Revoke(ctx context.Context, slug string) error {
	return errors.Join(i.mounter.Deny(ctx, slug), i.owner.Delete(ctx, slug))
}
