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

// La paire doit être utilisable telle quelle par un client SSH et par une forge.
// Les deux moitiés sont produites ensemble, et rien d'autre ne les vérifiera :
// une clé mal encodée ne se découvrirait qu'au premier clone d'un pipeline, dans
// un message qui désigne le dépôt et non la clé.
func TestLaPaireEngendreeEstRelisibleParUnClientSSH(t *testing.T) {
	pair, err := Generate("projet-test")
	if err != nil {
		t.Fatalf("génération: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(pair.Private)
	if err != nil {
		t.Fatalf("clé privée illisible: %v", err)
	}

	// Les deux moitiés doivent se correspondre. Deux clés valides mais
	// dépareillées seraient le pire cas : tout paraît correct, et l'accès est
	// refusé sans que rien ne dise pourquoi.
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pair.Public))
	if err != nil {
		t.Fatalf("clé publique illisible: %v", err)
	}
	if string(public.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("les deux moitiés ne se correspondent pas")
	}

	if !strings.HasPrefix(pair.Public, "ssh-ed25519 ") {
		t.Errorf("clé publique %q — attendu une ed25519", pair.Public)
	}
	if !strings.HasSuffix(pair.Public, " projet-test") {
		t.Errorf("clé publique %q — le commentaire doit nommer le projet", pair.Public)
	}
}

// Deux appels ne rendent jamais la même clé : une paire réutilisée entre deux
// projets annulerait tout le bénéfice de la séparation.
func TestChaqueGenerationRendUneClefDistincte(t *testing.T) {
	first, _ := Generate("a")
	second, _ := Generate("b")

	if first.Public == second.Public {
		t.Fatal("deux projets recevraient la même clé")
	}
}

// Le Secret déposé porte la matière sous la clé qu'attend Tekton, et rien
// d'autre. Un type ou un nom de champ erroné produirait un montage vide, et le
// clone échouerait comme si le dépôt était inaccessible.
func TestLeSecretPorteLaMatiereSousLaCleAttendueParTekton(t *testing.T) {
	kube := fake.NewSimpleClientset()
	owner := NewOwner(kube, "mse-tekton-pipelines")

	public, err := owner.Create(context.Background(), "mon-projet", "mon-projet", false)
	if err != nil {
		t.Fatalf("création: %v", err)
	}
	if public == "" {
		t.Fatal("la moitié publique doit être rendue à l'appelant")
	}

	secret, err := kube.CoreV1().Secrets("mse-tekton-pipelines").
		Get(context.Background(), SecretName("mon-projet"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}

	if secret.Type != corev1.SecretTypeSSHAuth {
		t.Errorf("type %q, attendu %q", secret.Type, corev1.SecretTypeSSHAuth)
	}
	if len(secret.Data[corev1.SSHAuthPrivateKey]) == 0 {
		t.Errorf("champ %q vide", corev1.SSHAuthPrivateKey)
	}
	// La moitié publique n'a rien à faire dans le Secret : le dashboard la garde
	// en base, et la dupliquer créerait deux vérités à tenir d'accord.
	if len(secret.Data) != 1 {
		t.Errorf("le secret porte %d champs, attendu la seule moitié privée", len(secret.Data))
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != ManagedBy {
		t.Errorf("étiquettes %v — la propriété doit être lisible", secret.Labels)
	}
}

// Le refus d'adopter, et c'est la règle centrale du lot G.
//
// Sans `get` sur les Secrets, le dashboard ne peut pas vérifier qu'un Secret
// portant le nom qu'il réclame lui appartient. La base dit que non — aucune clé
// connue pour ce projet — donc il vient d'ailleurs. L'écraser détruirait la clé
// de quelqu'un d'autre.
func TestUnSecretPreexistantNestJamaisAdopte(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SecretName("mon-projet"),
			Namespace: "mse-tekton-pipelines",
		},
		Data: map[string][]byte{corev1.SSHAuthPrivateKey: []byte("la clé de quelqu'un d'autre")},
	})
	owner := NewOwner(kube, "mse-tekton-pipelines")

	_, err := owner.Create(context.Background(), "mon-projet", "mon-projet", false)
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("erreur %v, attendu ErrNameTaken", err)
	}

	// Et surtout : la matière n'a pas bougé.
	secret, _ := kube.CoreV1().Secrets("mse-tekton-pipelines").
		Get(context.Background(), SecretName("mon-projet"), metav1.GetOptions{})
	if string(secret.Data[corev1.SSHAuthPrivateKey]) != "la clé de quelqu'un d'autre" {
		t.Error("le secret d'un tiers a été écrasé")
	}
}

// La rotation, elle, remplace — parce que la base atteste que la clé est nôtre.
// C'est le seul cas où un Secret existant est touché, et il est décidé par un
// état que le dashboard possède, jamais par une lecture du cluster.
func TestUneRotationRemplaceLaClefQuandLaBaseLAtteste(t *testing.T) {
	kube := fake.NewSimpleClientset()
	owner := NewOwner(kube, "mse-tekton-pipelines")
	ctx := context.Background()

	first, err := owner.Create(ctx, "mon-projet", "mon-projet", false)
	if err != nil {
		t.Fatalf("création: %v", err)
	}

	second, err := owner.Create(ctx, "mon-projet", "mon-projet", true)
	if err != nil {
		t.Fatalf("rotation: %v", err)
	}
	if first == second {
		t.Error("une rotation doit produire une clé neuve")
	}

	secret, _ := kube.CoreV1().Secrets("mse-tekton-pipelines").
		Get(ctx, SecretName("mon-projet"), metav1.GetOptions{})
	signer, err := ssh.ParsePrivateKey(secret.Data[corev1.SSHAuthPrivateKey])
	if err != nil {
		t.Fatalf("clé relue illisible: %v", err)
	}
	// La moitié rendue à l'appelant doit être celle du Secret déposé : les
	// désolidariser afficherait à l'écran une clé publique qui n'ouvre rien.
	public, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(second))
	if string(public.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("la clé affichée n'est pas celle déposée dans le secret")
	}
}

// Supprimer une clé absente aboutit : une suppression de projet ne doit pas
// rester bloquée sur un état que le dashboard ne peut de toute façon pas
// constater.
func TestSupprimerUneClefAbsenteAboutit(t *testing.T) {
	owner := NewOwner(fake.NewSimpleClientset(), "mse-tekton-pipelines")

	if err := owner.Delete(context.Background(), "jamais-cree"); err != nil {
		t.Errorf("suppression d'une clé absente: %v", err)
	}
}

// Le nom dérive du slug, déjà contraint par la migration 0026 à une forme qui
// est un nom de ressource Kubernetes valide.
func TestLeNomDuSecretDeriveDuSlug(t *testing.T) {
	if got := SecretName("hydra-sql-mse-theme"); got != "mse-dashboard-project-hydra-sql-mse-theme-git" {
		t.Errorf("SecretName = %q", got)
	}
}

// serviceAccount construit un compte de service portant déjà les identifiants du
// déploiement, comme celui de `mse-tekton`.
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
		t.Fatalf("relecture du compte de service: %v", err)
	}
	names := make([]string, 0, len(account.Secrets))
	for _, reference := range account.Secrets {
		names = append(names, reference.Name)
	}
	return names
}

// Autoriser un projet ajoute sa clé sans toucher aux identifiants du
// déploiement. Les élaguer casserait les pipelines qui les emploient, et le
// message qui en résulte désigne le clone, pas le compte de service.
func TestAutoriserUnProjetNeTouchePasAuxIdentifiantsDuDeploiement(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred", "git-credentials", "minio-credentials"))

	if err := mounter.Allow(context.Background(), "mon-projet"); err != nil {
		t.Fatalf("autorisation: %v", err)
	}

	got := mountable(t, kube)
	want := []string{"regcred", "git-credentials", "minio-credentials", SecretName("mon-projet")}
	if len(got) != len(want) {
		t.Fatalf("liste montable %v, attendu %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("liste montable %v, attendu %v", got, want)
			break
		}
	}
}

// Idempotent : l'observateur repasse au démarrage, et dupliquer l'entrée
// produirait un `patch` par démarrage sur un compte qui n'a pas bougé.
func TestAutoriserDeuxFoisNeDupliquePas(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred"))
	ctx := context.Background()

	_ = mounter.Allow(ctx, "mon-projet")
	_ = mounter.Allow(ctx, "mon-projet")

	if got := mountable(t, kube); len(got) != 2 {
		t.Errorf("liste montable %v — l'entrée est dupliquée", got)
	}
}

// Le garde-fou que doc 12 §12.7 réclame : « un secret créé hors de la liste
// montable ne doit pas devenir montable par accident ».
//
// La garantie est structurelle : ces méthodes prennent un **slug** et dérivent
// le nom elles-mêmes. Un appelant ne peut donc pas désigner `regcred`, même en
// le passant en toutes lettres — il obtiendrait une entrée pour un projet nommé
// `regcred`, distincte, et l'identifiant partagé resterait intact.
func TestUnSecretDuDeploiementNePeutPasEtreDesigneParErreur(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred", "git-credentials"))

	// Un appelant fautif passe le nom d'un identifiant partagé comme s'il
	// s'agissait d'un slug.
	if err := mounter.Allow(context.Background(), "regcred"); err != nil {
		t.Fatalf("autorisation: %v", err)
	}

	got := mountable(t, kube)
	// Trois entrées, pas deux : l'entrée ajoutée est celle d'un projet, et non
	// une seconde référence à l'identifiant partagé.
	if len(got) != 3 || got[2] != SecretName("regcred") {
		t.Errorf("liste montable %v — le nom aurait dû être dérivé", got)
	}

	// Et retirer ce « projet » ne retire pas l'identifiant partagé.
	if err := mounter.Deny(context.Background(), "regcred"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	after := mountable(t, kube)
	if len(after) != 2 || after[0] != "regcred" {
		t.Errorf("liste montable %v — un identifiant du déploiement a été élagué", after)
	}
}

// Retirer une clé jamais autorisée n'écrit rien : un `patch` inutile sur un
// compte de service partagé est un conflit gratuit avec la kustomization qui le
// déclare.
func TestRetirerUneClefAbsenteNecritRien(t *testing.T) {
	kube, mounter := mounterFor(t, serviceAccount("regcred"))

	if err := mounter.Deny(context.Background(), "jamais-autorise"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if got := mountable(t, kube); len(got) != 1 {
		t.Errorf("liste montable %v", got)
	}
}

// La clé doit être montable par **chaque** compte que le catalogue emploie.
//
// N'en tenir qu'un la rendrait montable pour certains pipelines du projet et pas
// pour les autres, et l'échec désignerait le clone plutôt que le montage : le
// message parle du dépôt, aucun conteneur n'a démarré, et on part chercher une
// clé manquante côté forge.
func TestLaClefEstMontableParTousLesComptesDuCatalogue(t *testing.T) {
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
			t.Errorf("compte %q: la clé du projet n'y est pas montable", name)
		}
	}
}

// Un compte absent ne fait pas échouer les autres : le catalogue peut nommer un
// compte que le déploiement n'a pas encore créé, et perdre l'autorisation sur
// ceux qui existent aggraverait la situation.
func TestUnCompteAbsentNempechePasLesAutres(t *testing.T) {
	kube := fake.NewSimpleClientset(serviceAccount("regcred"))
	mounter := NewMounter(kube, "mse-tekton-pipelines", "tekton-kubectl", "jamais-cree")

	err := mounter.Allow(context.Background(), "mon-projet")
	if err == nil {
		t.Error("l'absence d'un compte doit être signalée")
	}

	account, _ := kube.CoreV1().ServiceAccounts("mse-tekton-pipelines").
		Get(context.Background(), "tekton-kubectl", metav1.GetOptions{})
	if len(account.Secrets) != 2 {
		t.Errorf("compte existant %v — l'autorisation aurait dû y aboutir", account.Secrets)
	}
}
