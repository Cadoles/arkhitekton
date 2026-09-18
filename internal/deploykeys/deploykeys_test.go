package deploykeys

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The pair must be usable as it is by an SSH client and by a forge. Both halves are produced
// together, and nothing else will check them: a badly encoded key would be discovered only at a
// pipeline's first clone, in a message that names the repository and not the key.
func TestTheGeneratedPairIsReadableByAnSSHClient(t *testing.T) {
	pair, err := Generate("projet-test")
	if err != nil {
		t.Fatalf("generation: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(pair.Private)
	if err != nil {
		t.Fatalf("private key unreadable: %v", err)
	}

	// Both halves must match. Two valid but mismatched keys would be the worst case: everything
	// looks right, and access is refused with nothing saying why.
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pair.Public))
	if err != nil {
		t.Fatalf("public key unreadable: %v", err)
	}
	if string(public.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("the two halves do not match")
	}

	if !strings.HasPrefix(pair.Public, "ssh-ed25519 ") {
		t.Errorf("public key %q — expected an ed25519 one", pair.Public)
	}
	if !strings.HasSuffix(pair.Public, " projet-test") {
		t.Errorf("public key %q — the comment must name the project", pair.Public)
	}
}

// Two calls never return the same key: a pair reused between two projects would cancel the whole
// benefit of the separation.
func TestEachGenerationReturnsADistinctKey(t *testing.T) {
	first, _ := Generate("a")
	second, _ := Generate("b")

	if first.Public == second.Public {
		t.Fatal("two projects would receive the same key")
	}
}

// The Secret deposited carries the material under the key Tekton expects, and nothing else. A
// wrong type or field name would produce an empty mount, and the clone would fail as if the
// repository were unreachable.
// The Secret must carry the key under the name **SSH** reads, not only under the one the type
// imposes.
//
// # This test said the opposite, and it passed
//
// It required a single field, `ssh-privatekey`, calling it "the key Tekton expects". That was
// true of the `kubernetes.io/ssh-auth` **type** and false of the `ssh-directory` workspace, which
// is copied into `~/.ssh` where OpenSSH loads only `id_ed25519` or `id_rsa`.
//
// The consequence: every build failed with `Permission denied (publickey)` after the move to
// per-project keys, although the public key was indeed declared on the repository. The green test
// had frozen the mistake.
func TestTheSecretCarriesTheMaterialUnderTheNameSSHReads(t *testing.T) {
	kube := fake.NewSimpleClientset()
	owner := NewOwner(kube, "mse-tekton-pipelines")

	public, err := owner.Create(context.Background(), "mon-projet", "mon-projet", false)
	if err != nil {
		t.Fatalf("creation: %v", err)
	}
	if public == "" {
		t.Fatal("the public half must be returned to the caller")
	}

	secret, err := kube.CoreV1().Secrets("mse-tekton-pipelines").
		Get(context.Background(), SecretName("mon-projet"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}

	if secret.Type != corev1.SecretTypeSSHAuth {
		t.Errorf("type %q, expected %q", secret.Type, corev1.SecretTypeSSHAuth)
	}
	// The name the type imposes, so that the object stays typed.
	if len(secret.Data[corev1.SSHAuthPrivateKey]) == 0 {
		t.Errorf("champ %q vide", corev1.SSHAuthPrivateKey)
	}
	// And the one SSH reads. It is this one that was missing.
	if len(secret.Data[PrivateKeyFile]) == 0 {
		t.Fatalf("field %q missing: SSH would load no identity", PrivateKeyFile)
	}
	// The same material under both names: two distinct keys would mean one of them opens nothing.
	if string(secret.Data[PrivateKeyFile]) != string(secret.Data[corev1.SSHAuthPrivateKey]) {
		t.Error("the two fields carry different keys")
	}
	// The public half has no business in the Secret: the dashboard keeps it in the database, and
	// duplicating it would create two truths to keep in agreement.
	if len(secret.Data) != 2 {
		t.Errorf("the secret carries %d fields, expected the private half alone "+
			"under both its names", len(secret.Data))
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != ManagedBy {
		t.Errorf("labels %v — ownership must be readable", secret.Labels)
	}
}

// The refusal to adopt, and it is the central rule.
//
// Without `get` on Secrets, the dashboard cannot check that a Secret carrying the name it claims
// belongs to it. The database says it does not — no known key for this project — so it comes
// from elsewhere. Overwriting it would destroy someone else's key.
func TestAPreExistingSecretIsNeverAdopted(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SecretName("mon-projet"),
			Namespace: "mse-tekton-pipelines",
		},
		Data: map[string][]byte{corev1.SSHAuthPrivateKey: []byte("someone else's key")},
	})
	owner := NewOwner(kube, "mse-tekton-pipelines")

	_, err := owner.Create(context.Background(), "mon-projet", "mon-projet", false)
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("error %v, expected ErrNameTaken", err)
	}

	// And above all: the material has not moved.
	secret, _ := kube.CoreV1().Secrets("mse-tekton-pipelines").
		Get(context.Background(), SecretName("mon-projet"), metav1.GetOptions{})
	if string(secret.Data[corev1.SSHAuthPrivateKey]) != "someone else's key" {
		t.Error("a third party's secret was overwritten")
	}
}

// The rotation does replace — because the database attests the key is ours. It is the only case
// where an existing Secret is touched, and it is decided by a state the dashboard owns, never by
// a read of the cluster.
func TestARotationReplacesTheKeyWhenTheDatabaseAttestsIt(t *testing.T) {
	kube := fake.NewSimpleClientset()
	owner := NewOwner(kube, "mse-tekton-pipelines")
	ctx := context.Background()

	first, err := owner.Create(ctx, "mon-projet", "mon-projet", false)
	if err != nil {
		t.Fatalf("creation: %v", err)
	}

	second, err := owner.Create(ctx, "mon-projet", "mon-projet", true)
	if err != nil {
		t.Fatalf("rotation: %v", err)
	}
	if first == second {
		t.Error("a rotation must produce a new key")
	}

	secret, _ := kube.CoreV1().Secrets("mse-tekton-pipelines").
		Get(ctx, SecretName("mon-projet"), metav1.GetOptions{})
	if len(secret.Data[PrivateKeyFile]) == 0 {
		t.Fatalf("field %q missing after the rotation: the new key would be useless",
			PrivateKeyFile)
	}

	signer, err := ssh.ParsePrivateKey(secret.Data[corev1.SSHAuthPrivateKey])
	if err != nil {
		t.Fatalf("key read back unreadable: %v", err)
	}
	// The half returned to the caller must be that of the Secret deposited: separating them would
	// display on screen a public key that opens nothing.
	public, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(second))
	if string(public.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("the key displayed is not the one deposited in the secret")
	}
}

// Deleting an absent key completes: deleting a project must not stay stuck on a state the
// dashboard cannot observe anyway.
func TestDeletingAnAbsentKeyCompletes(t *testing.T) {
	owner := NewOwner(fake.NewSimpleClientset(), "mse-tekton-pipelines")

	if err := owner.Delete(context.Background(), "jamais-cree"); err != nil {
		t.Errorf("deleting an absent key: %v", err)
	}
}

// The name derives from the slug, already constrained by a migration to a form that is a valid
// Kubernetes resource name.
func TestTheSecretNameDerivesFromTheSlug(t *testing.T) {
	if got := SecretName("hydra-sql-mse-theme"); got != "arkhitekton-project-hydra-sql-mse-theme-git" {
		t.Errorf("SecretName = %q", got)
	}
}

// serviceAccount builds a service account already carrying the deployment's credentials, like
// `mse-tekton`'s.
func serviceAccount(secrets ...string) *corev1.ServiceAccount {
	references := make([]corev1.ObjectReference, 0, len(secrets))
	for _, name := range secrets {
		references = append(references, corev1.ObjectReference{Name: name})
	}
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tekton-kubectl",
			Namespace:   "mse-tekton-pipelines",
			Annotations: map[string]string{"kubernetes.io/enforce-mountable-secrets": "true"},
		},
		Secrets: references,
	}
}

func mounterFor(t *testing.T, account *corev1.ServiceAccount) (*fake.Clientset, *Mounter) {
	t.Helper()
	kube := fake.NewSimpleClientset(account)
	return kube, NewMounter(kube, "mse-tekton-pipelines", "tekton-kubectl")
}

func mountable(t *testing.T, kube *fake.Clientset) []string {
	t.Helper()
	account, err := kube.CoreV1().ServiceAccounts("mse-tekton-pipelines").
		Get(context.Background(), "tekton-kubectl", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-reading the service account: %v", err)
	}
	names := make([]string, 0, len(account.Secrets))
	for _, reference := range account.Secrets {
		names = append(names, reference.Name)
	}
	return names
}

// Allowing a project adds its key without touching the deployment's credentials. Pruning them
// would break the pipelines that use them, and the resulting message names the clone, not the
// service account.
func TestAllowingAProjectDoesNotTouchTheDeploymentCredentials(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred", "git-credentials", "minio-credentials"))

	if err := mounter.Allow(context.Background(), "mon-projet"); err != nil {
		t.Fatalf("autorisation: %v", err)
	}

	got := mountable(t, kube)
	want := []string{"regcred", "git-credentials", "minio-credentials", SecretName("mon-projet")}
	if len(got) != len(want) {
		t.Fatalf("mountable list %v, expected %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mountable list %v, expected %v", got, want)
			break
		}
	}
}

// Idempotent: the observer comes round again at start-up, and duplicating the entry would produce
// one `patch` per start-up on an account that has not moved.
func TestAllowingTwiceDoesNotDuplicate(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred"))
	ctx := context.Background()

	_ = mounter.Allow(ctx, "mon-projet")
	_ = mounter.Allow(ctx, "mon-projet")

	if got := mountable(t, kube); len(got) != 2 {
		t.Errorf("mountable list %v — the entry is duplicated", got)
	}
}

// The guard rail: a secret created outside the mountable list must not become mountable by
// accident.
//
// The guarantee is structural: these methods take a **slug** and derive the name themselves. A
// caller therefore cannot name `regcred`, even by passing it in full — it would get an entry for a
// project named `regcred`, a distinct one, and the shared credential would stay intact.
func TestADeploymentSecretCannotBeNamedByMistake(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred", "git-credentials"))

	// A faulty caller passes the name of a shared credential as if it were a slug.
	if err := mounter.Allow(context.Background(), "regcred"); err != nil {
		t.Fatalf("autorisation: %v", err)
	}

	got := mountable(t, kube)
	// Three entries, not two: the entry added is a project's, and not a second reference to the
	// shared credential.
	if len(got) != 3 || got[2] != SecretName("regcred") {
		t.Errorf("mountable list %v — the name should have been derived", got)
	}

	// And removing that "project" does not remove the shared credential.
	if err := mounter.Deny(context.Background(), "regcred"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	after := mountable(t, kube)
	if len(after) != 2 || after[0] != "regcred" {
		t.Errorf("mountable list %v — a deployment credential was pruned", after)
	}
}

// Removing a key that was never allowed writes nothing: a pointless `patch` on a shared service
// account is a gratuitous conflict with the kustomization that declares it.
func TestRemovingAnAbsentKeyWritesNothing(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred"))

	if err := mounter.Deny(context.Background(), "jamais-autorise"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if got := mountable(t, kube); len(got) != 1 {
		t.Errorf("liste montable %v", got)
	}
}

// The key must be mountable by **every** account the catalogue uses.
//
// Keeping only one would make it mountable for some of the project's pipelines and not for the
// others, and the failure would name the clone rather than the mount: the message speaks of the
// repository, no container started, and one goes hunting for a missing key on the forge's side.
func TestTheKeyIsMountableByEveryAccountOfTheCatalogue(t *testing.T) {
	kube := fake.NewSimpleClientset(
		serviceAccount("regcred"),
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "mse-tekton-pipelines"},
		},
	)
	mounter := NewMounter(kube, "mse-tekton-pipelines", "tekton-kubectl", "default")

	if err := mounter.Allow(context.Background(), "mon-projet"); err != nil {
		t.Fatalf("autorisation: %v", err)
	}

	for _, name := range []string{"tekton-kubectl", "default"} {
		account, _ := kube.CoreV1().ServiceAccounts("mse-tekton-pipelines").
			Get(context.Background(), name, metav1.GetOptions{})
		found := false
		for _, reference := range account.Secrets {
			if reference.Name == SecretName("mon-projet") {
				found = true
			}
		}
		if !found {
			t.Errorf("account %q: the project's key is not mountable there", name)
		}
	}
}

// A missing account does not fail the others: the catalogue may name an account the deployment
// has not created yet, and losing the authorization on those that exist would make things worse.
func TestAMissingAccountDoesNotStopTheOthers(t *testing.T) {
	kube := fake.NewSimpleClientset(serviceAccount("regcred"))
	mounter := NewMounter(kube, "mse-tekton-pipelines", "tekton-kubectl", "jamais-cree")

	err := mounter.Allow(context.Background(), "mon-projet")
	if err == nil {
		t.Error("a missing account must be reported")
	}

	account, _ := kube.CoreV1().ServiceAccounts("mse-tekton-pipelines").
		Get(context.Background(), "tekton-kubectl", metav1.GetOptions{})
	if len(account.Secrets) != 2 {
		t.Errorf("existing account %v — the authorization should have completed there", account.Secrets)
	}
}
