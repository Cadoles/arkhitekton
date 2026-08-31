package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/deploykeys"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeKeyStore struct {
	pending []store.PendingKey
	saved   map[int64]string
	audits  int
}

func (f *fakeKeyStore) ProjectsAwaitingKey(context.Context) ([]store.PendingKey, error) {
	return f.pending, nil
}

func (f *fakeKeyStore) SetDeployKey(_ context.Context, id int64, public string) error {
	if f.saved == nil {
		f.saved = map[int64]string{}
	}
	f.saved[id] = public
	return nil
}

func (f *fakeKeyStore) Audit(context.Context, string, string, string, map[string]any) error {
	f.audits++
	return nil
}

type fakeKeyIssuer struct {
	// byslug rend une erreur pour les slugs qui y figurent.
	failures map[string]error
	issued   []string
	known    map[string]bool
}

func (f *fakeKeyIssuer) Issue(_ context.Context, slug string, known bool) (string, error) {
	if err := f.failures[slug]; err != nil {
		return "", err
	}
	f.issued = append(f.issued, slug)
	if f.known == nil {
		f.known = map[string]bool{}
	}
	f.known[slug] = known
	return "ssh-ed25519 AAAA " + slug, nil
}

// Le cas nominal : chaque projet en attente reçoit sa clé, et la moitié publique
// est enregistrée.
func TestChaqueProjetEnAttenteRecoitSaClef(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "mse", Known: false},
		{ID: 2, Slug: "hydra-sql", Known: false},
	}}
	issuer := &fakeKeyIssuer{}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(issuer.issued) != 2 {
		t.Fatalf("%d clé(s) posée(s), attendu 2", len(issuer.issued))
	}
	if projects.saved[1] == "" || projects.saved[2] == "" {
		t.Errorf("clés enregistrées: %v", projects.saved)
	}
}

// Une rotation se distingue d'une première pose, et le poseur doit le savoir :
// c'est ce qui l'autorise à remplacer un Secret existant plutôt que de refuser
// de l'adopter.
func TestUneRotationEstSignaleeAuPoseur(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "mse", Known: true},
	}}
	issuer := &fakeKeyIssuer{}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if !issuer.known["mse"] {
		t.Error("la rotation n'a pas été signalée: le poseur refusera d'adopter")
	}
}

// Un projet dont le nom est déjà pris ne bloque pas les autres.
//
// C'est une situation qu'un humain doit trancher — un Secret homonyme que le
// dashboard n'a pas créé —, et s'arrêter dessus priverait de clé tous les
// projets qui viennent après dans la liste.
func TestUnNomDejaPrisNeBloquePasLesAutres(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "occupe"},
		{ID: 2, Slug: "libre"},
	}}
	issuer := &fakeKeyIssuer{
		failures: map[string]error{"occupe": deploykeys.ErrNameTaken},
	}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(issuer.issued) != 1 || issuer.issued[0] != "libre" {
		t.Errorf("clés posées %v — le projet suivant doit être servi", issuer.issued)
	}
	// Et surtout : rien n'est enregistré pour celui qui a échoué. L'inscrire
	// affirmerait une propriété que le dashboard n'a pas, et une rotation
	// ultérieure écraserait le Secret d'un tiers.
	if _, recorded := projects.saved[1]; recorded {
		t.Error("une clé non posée a été enregistrée: la propriété serait fausse")
	}
}

// Une panne de cluster n'inscrit rien non plus, pour la même raison.
func TestUneClefNonPoseeNestPasEnregistree(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{{ID: 1, Slug: "mse"}}}
	issuer := &fakeKeyIssuer{
		failures: map[string]error{"mse": errors.New("API injoignable")},
	}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(projects.saved) != 0 {
		t.Errorf("enregistrements %v — aucune clé n'a été posée", projects.saved)
	}
	if projects.audits != 0 {
		t.Error("une pose échouée ne doit pas être journalisée comme faite")
	}
}
