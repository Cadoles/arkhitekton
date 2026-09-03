package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
)

// fakeVerificationStore retient ce qui est mis en file après une construction.
type fakeVerificationStore struct {
	enqueued  []store.JobRequest
	enqueueEr error
	// state simule la release et son écriture conditionnelle.
	state    string
	advances int
	// jobRelease associe un job à sa release. Nil, tous les jobs en rendent une
	// vide — le comportement des tests qui ne s'y intéressent pas.
	jobRelease map[int64]store.JobReleaseContext
	// builds est le décompte des constructions rendu à l'observateur. Zéro
	// valeur : une seule construction réussie, ce qui est le cas des tests qui ne
	// s'intéressent pas à la multiplicité des images.
	builds store.BuildTally
}

func (f *fakeVerificationStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	if f.enqueueEr != nil {
		return 0, f.enqueueEr
	}
	f.enqueued = append(f.enqueued, request)
	return int64(len(f.enqueued)), nil
}

func verificationCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Pipelines: []catalog.Pipeline{
			{
				Kind: "static-tests", Verification: true, Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			// Les tests unitaires ont leur propre pipeline : deux questions
			// distinctes, deux signaux que le score pondère séparément.
			{
				Kind: "unit-tests", Verification: true, Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			{
				Kind: "security-audit", Verification: true, Params: map[string]catalog.Param{
					// L'audit nomme sa révision autrement que les tests statiques,
					// et réclame les images déjà publiées plutôt que de les
					// reconstruire.
					// Trois noms pour trois natures : c'est la déclaration, et non le
					// nom du paramètre, qui dit ce que l'enchaînement doit y mettre.
					"app-revision": {
						Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"portal-image": {
						Source: catalog.SourceInput, Required: true,
						Suggest: catalog.SuggestImage, Image: "portal"},
					"consumer-image": {
						Source: catalog.SourceInput, Required: true,
						Suggest: catalog.SuggestImage, Image: "portal-cli"},
					"app-repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			// La révision de l'infrastructure de déploiement vient de la
			// configuration : c'est ce qui rend ce pipeline enchaînable. Tant
			// qu'elle était une entrée, le dashboard ne savait pas la renseigner.
			{
				Kind: "integration-tests", Verification: true, Params: map[string]catalog.Param{
					"mse-revision": {
						Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					"mse-kustom-revision": {Source: catalog.SourceConfig, Key: "forge.mseKustom.revision"},
					// Le pipeline déploie des images déjà publiées : il en reçoit
					// le tag, jamais de quoi les reconstruire.
					"image-tag": {
						Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestReleaseTag},
				},
			},
		},
	}
}

func sinkFor(store *fakeVerificationStore, pipelines *catalog.Catalog) *runSink {
	sink := &runSink{
		logger: quietLogger(), catalog: pipelines,
		registryProject: "reg.cadoles.com/vcarroy",
	}
	sink.verification = store
	// Le moteur de plans, sur un magasin en mémoire : l'enchaînement passe
	// désormais par lui, et le juge doit l'exercer plutôt que de le contourner.
	sink.engine = NewVerificationEngine(sink, newFakePlanStore(), quietLogger())
	return sink
}

// Une construction réussie enchaîne les vérifications sur **le commit
// construit**, jamais sur la branche d'où il vient : celle-ci a pu bouger depuis
// le build, et les tests porteraient alors sur autre chose que l'image (B17).
func TestLaVerificationPorteSurLeCommitConstruit(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())

	launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1",
		GitRef: "main", GitCommit: "abc123def456",
	})

	if launched != 4 {
		t.Fatalf("%d vérifications lancées, attendu 4", launched)
	}

	kinds := map[string]store.JobRequest{}
	for _, job := range fake.enqueued {
		kinds[job.Kind] = job
	}
	if _, ok := kinds["static-tests"]; !ok {
		t.Error("static-tests devrait être lancé")
	}
	if _, ok := kinds["unit-tests"]; !ok {
		t.Error("unit-tests devrait être lancé")
	}
	if _, ok := kinds["security-audit"]; !ok {
		t.Error("security-audit devrait être lancé")
	}
	integration, ok := kinds["integration-tests"]
	if !ok {
		t.Error("integration-tests devrait être lancé")
	}
	// Le pipeline d'intégration nomme sa révision `mse-revision`. C'est encore le
	// commit construit : le déploiement qu'il éprouve doit être celui de la
	// release, pas celui de la branche dont elle est issue (B17).
	if integration.Params["mse-revision"] != "abc123def456" {
		t.Errorf("params de integration-tests: %v", integration.Params)
	}
	// `mse-kustom-revision` vient de la configuration, pas de l'enchaînement :
	// l'ordonnanceur la résout au lancement, et la fournir ici la figerait au
	// moment de la construction.
	if _, present := integration.Params["mse-kustom-revision"]; present {
		t.Error("mse-kustom-revision ne doit pas être renseignée à l'enchaînement")
	}
	// Le tag des images à déployer est celui de la release : ce sont celles que
	// sa construction vient de publier, et non une reconstruction (B17).
	if integration.Params["image-tag"] != "3.2.0-rc1" {
		t.Errorf("image-tag %q, attendu le tag de la release", integration.Params["image-tag"])
	}

	static := kinds["static-tests"]
	if static.Params["revision"] != "abc123def456" {
		t.Errorf("params de static-tests: %v — attendu le commit, pas la branche", static.Params)
	}

	// L'audit nomme sa révision `app-revision`. Sans ce nom, il retombait sur son
	// défaut et scannait `CNOUS/mse` à `k8s-unstable` — donc autre chose que la
	// release, en rendant un verdict qu'on rapportait à la mauvaise chose.
	audit := kinds["security-audit"]
	if audit.Params["app-revision"] != "abc123def456" {
		t.Errorf("params de security-audit: %v — attendu le commit en app-revision", audit.Params)
	}
	// L'audit analyse les images **publiées** par la construction, au tag de la
	// release. Reconstruire pour scanner produirait un artefact différent de celui
	// qui sera déployé (B17).
	if got := audit.Params["portal-image"]; got != "reg.cadoles.com/vcarroy/portal:3.2.0-rc1" {
		t.Errorf("image portal %q", got)
	}
	// `portal-cli` depuis la migration FrankenPHP de `mse` : la cible `consumer`
	// du Dockerfile a disparu, remplacée par `cli`, et l'image qu'elle publie a
	// été renommée avec elle.
	if got := audit.Params["consumer-image"]; got != "reg.cadoles.com/vcarroy/portal-cli:3.2.0-rc1" {
		t.Errorf("image consumer %q", got)
	}
	if static.ReleaseID == nil || *static.ReleaseID != 7 {
		t.Error("les vérifications doivent porter leur release, sans quoi leur conclusion ne fait rien avancer")
	}
}

// Sans commit résolu, la vérification retombe sur la référence : moins bon, mais
// mieux que de ne rien lancer.
func TestSansCommitLaVerificationRetombeSurLaReference(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())

	launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", GitRef: "main",
	})

	if launched != 4 {
		t.Fatalf("%d vérifications lancées", launched)
	}
	for _, job := range fake.enqueued {
		if job.Kind == "static-tests" && job.Params["revision"] != "main" {
			t.Errorf("révision %q, attendu le repli sur la référence", job.Params["revision"])
		}
	}
}

// Aucune référence du tout : rien à tester, et le dire vaut mieux que d'enfiler
// un run qui échouera au clone.
func TestSansReferenceAucuneVerificationNePart(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())

	if launched := sink.launchVerification(context.Background(),
		store.JobReleaseContext{ReleaseID: 7, Tag: "3.2.0-rc1"}); launched != 0 {
		t.Errorf("%d vérifications lancées, attendu 0", launched)
	}
	if len(fake.enqueued) != 0 {
		t.Errorf("jobs enfilés: %+v", fake.enqueued)
	}
}

// Un échec de mise en file est consigné, pas propagé : la construction a réussi,
// et perdre ce fait parce qu'un test n'a pas pu être enfilé serait pire.
func TestUnEchecDeMiseEnFileNeFaitPasEchouerLaConclusion(t *testing.T) {
	fake := &fakeVerificationStore{enqueueEr: errors.New("file indisponible")}
	sink := sinkFor(fake, verificationCatalog())

	if launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", GitCommit: "abc123",
	}); launched != 0 {
		t.Errorf("%d vérifications lancées malgré l'échec", launched)
	}
}

// Sans catalogue branché, rien ne part — et l'observation continue. Un
// observateur qui tomberait faute de catalogue cesserait aussi de projeter les
// runs, ce qui est bien pire que de ne pas enchaîner.
func TestSansCatalogueRienNePart(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, nil)

	if launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", GitCommit: "abc123",
	}); launched != 0 {
		t.Errorf("%d vérifications lancées sans catalogue", launched)
	}
}

func (f *fakeVerificationStore) AdvanceReleaseState(_ context.Context, _ int64, from, to string) (bool, error) {
	if f.state != from {
		return false, nil
	}
	f.state = to
	f.advances++
	return true, nil
}

func (f *fakeVerificationStore) CountBuilds(
	_ context.Context, _ int64, _ string,
) (store.BuildTally, error) {
	if f.builds == (store.BuildTally{}) {
		return store.BuildTally{Succeeded: 1}, nil
	}
	return f.builds, nil
}

func (f *fakeVerificationStore) JobRelease(_ context.Context, jobID int64) (store.JobReleaseContext, error) {
	if f.jobRelease == nil {
		return store.JobReleaseContext{}, nil
	}
	linked, ok := f.jobRelease[jobID]
	if !ok {
		return store.JobReleaseContext{}, store.ErrJobWithoutRelease
	}
	return linked, nil
}

// L'observateur redélivre les runs terminés : resynchronisation de l'informer,
// redémarrage du worker. Une même construction conclut donc plusieurs fois, et
// les vérifications ne doivent partir qu'une.
//
// Constaté avant correction : cinq paires de tests pour une seule construction,
// parce que le lancement précédait le contrôle de transition.
func TestUneConstructionNeLanceSesVerificationsQuUneFois(t *testing.T) {
	fake := &fakeVerificationStore{state: "building"}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	linked := store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "build",
		State: "building", GitCommit: "abc123",
	}

	// Première conclusion : la release avance et les tests partent.
	if err := sink.advanceRelease(context.Background(), linked, store.JobSucceeded); err != nil {
		t.Fatalf("première conclusion: %v", err)
	}
	if fake.state != "verifying" {
		t.Fatalf("état %q, attendu verifying", fake.state)
	}
	premier := len(fake.enqueued)
	if premier != 4 {
		t.Fatalf("%d vérifications lancées, attendu 4", premier)
	}

	// Redélivrances : l'état lu est resté `building` côté appelant, mais la base
	// est passée à `verifying`. Rien ne doit repartir.
	for i := 0; i < 3; i++ {
		if err := sink.advanceRelease(context.Background(), linked, store.JobSucceeded); err != nil {
			t.Fatalf("redélivrance %d: %v", i, err)
		}
	}
	if len(fake.enqueued) != premier {
		t.Errorf("%d vérifications au total, attendu %d — les redélivrances en ont relancé",
			len(fake.enqueued), premier)
	}
}

// Le défaut qui a produit deux jeux de vérifications pour une seule
// construction : la table des transitions autorise `failed -> verifying`, pour
// qu'un opérateur puisse reprendre une release après un échec. La conclusion de
// construction empruntait ce chemin comme n'importe quel autre.
//
// Déroulé constaté : la construction réussit, la release passe en `verifying`,
// l'audit échoue, la release retombe en `failed`. Le worker redémarre,
// l'informer redélivre la construction terminée, et la conclusion relance tout.
//
// Une conclusion de construction ne vaut que depuis `building`.
func TestUneConstructionNeConclutRienDepuisUneReleaseDejaEchouee(t *testing.T) {
	fake := &fakeVerificationStore{state: "failed"}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	err := sink.advanceRelease(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "build",
		State: "failed", GitCommit: "abc123",
	}, store.JobSucceeded)
	if err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	if fake.state != "failed" {
		t.Errorf("état %q — une redélivrance a fait bouger la release", fake.state)
	}
	if len(fake.enqueued) != 0 {
		t.Errorf("%d vérifications relancées: %+v", len(fake.enqueued), fake.enqueued)
	}
}

// Même règle pour les autres états. `ready` et `released` sont les plus
// dangereux : y relancer des vérifications les ferait retomber en `verifying`,
// et une release validée redeviendrait non validée sans que personne l'ait
// décidé.
func TestSeuleUneReleaseEnConstructionEstConclueParSonBuild(t *testing.T) {
	for _, state := range []string{"draft", "verifying", "ready", "released", "cancelled"} {
		fake := &fakeVerificationStore{state: state}
		sink := sinkFor(fake, verificationCatalog())
		sink.releases = fake

		if err := sink.advanceRelease(context.Background(), store.JobReleaseContext{
			ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "build",
			State: state, GitCommit: "abc123",
		}, store.JobSucceeded); err != nil {
			t.Fatalf("état %s: %v", state, err)
		}

		if fake.state != state {
			t.Errorf("état %s devenu %s", state, fake.state)
		}
		if len(fake.enqueued) != 0 {
			t.Errorf("état %s: %d vérifications lancées", state, len(fake.enqueued))
		}
	}
}

// Et une construction en échec conclut toujours, elle : c'est le seul chemin par
// lequel une release apprend que sa construction a échoué.
func TestUneConstructionEchoueeFaitEchouerSaRelease(t *testing.T) {
	fake := &fakeVerificationStore{state: "building"}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	if err := sink.advanceRelease(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "build",
		State: "building", GitCommit: "abc123",
	}, store.JobFailed); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	if fake.state != "failed" {
		t.Errorf("état %q, attendu failed", fake.state)
	}
	if len(fake.enqueued) != 0 {
		t.Error("une construction échouée ne lance pas de vérifications")
	}
}

// Le second visage du même défaut. Corriger le **nom** des runs empêche une base
// neuve d'écraser un run existant ; il reste le **label**, par lequel
// l'observateur rattache un run à un job. Un run laissé dans le cluster porte
// `job-id: 7` ; la base suivante crée à son tour un job 7 ; l'observateur lit le
// label et conclut ce job avec le verdict d'un run qui ne le concerne pas.
//
// Constaté : `mse-static-tests-7`, échoué quatre heures plus tôt sous une base
// précédente, a fait passer en échec le job 7 d'une base créée depuis — un job
// qui n'avait pas encore été lancé, et dont aucun run n'existait.
func TestUnRunDUneAutreBaseNeConclutAucunJob(t *testing.T) {
	sink := &runSink{logger: quietLogger(), instance: "1d82ae"}

	disowned := sink.disown(tekton.Run{
		Name: "mse-static-tests-7", JobID: 7, Instance: "",
		Status: tekton.StatusFailed, Reason: "Failed",
	})

	if disowned.JobID != 0 {
		t.Errorf("job %d encore rattaché", disowned.JobID)
	}
	if !disowned.Observed {
		t.Error("un run d'une autre base est observé, pas piloté")
	}
	// Le run reste projeté : il a existé, ses journaux ont une valeur, et le
	// masquer ferait disparaître de l'historique ce que le cluster détient encore.
	if disowned.Name != "mse-static-tests-7" || disowned.Status != tekton.StatusFailed {
		t.Errorf("le run a été altéré au-delà du rattachement: %+v", disowned)
	}
}

// Un run de la base courante garde son rattachement, sans quoi plus rien
// n'avancerait.
func TestUnRunDeLaBaseCouranteResteRattache(t *testing.T) {
	sink := &runSink{logger: quietLogger(), instance: "1d82ae"}

	run := tekton.Run{
		Name: "mse-1d82ae-static-tests-7", JobID: 7, Instance: "1d82ae",
		Status: tekton.StatusSucceeded,
	}
	if got := sink.disown(run); got.JobID != 7 || got.Observed {
		t.Errorf("rattachement perdu: %+v", got)
	}
}

// Un run qui n'a jamais été piloté — webhook Gitea, CronJob — traverse inchangé,
// quelle que soit la base.
func TestUnRunObserveTraverseInchange(t *testing.T) {
	sink := &runSink{logger: quietLogger(), instance: "1d82ae"}

	run := tekton.Run{Name: "e2e-test-run-f8m8c", JobID: 0, Observed: true}
	if got := sink.disown(run); got.JobID != 0 || !got.Observed {
		t.Errorf("run observé altéré: %+v", got)
	}
}

// Le catalogue réellement livré, confronté à ce que l'enchaînement lance.
//
// Une famille de vérification qui ne déclare aucun artefact tourne, échoue ou
// réussit, et le dashboard n'en sait rien : son signal reste « non mesuré »
// pendant que dix scanners ou trois cents tests s'exécutent. C'était le cas de
// `security-audit` et de `integration-tests`, qui publiaient dans le bucket des
// pipelines — là où le lecteur ne regarde pas.
//
// La règle vaut pour tout ce qui est enchaîné : lancer une vérification dont on
// ne lira pas le verdict revient à ne pas la lancer, en plus cher.
func TestToutCeQuiEstEnchaineRendUnResultatLisible(t *testing.T) {
	shipped := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(shipped); err != nil {
		t.Skip("catalogue livré introuvable")
	}

	pipelines, err := catalog.Load(shipped)
	if err != nil {
		t.Fatalf("catalogue livré: %v", err)
	}

	kinds := pipelines.VerificationKinds()
	if len(kinds) == 0 {
		t.Fatal("le catalogue livré n'enchaîne aucune vérification : une construction réussie conclurait sur rien")
	}

	// La règle « une vérification déclare un artefact » est désormais tenue par la
	// validation du catalogue, que `Load` applique : ce test échouerait plus haut,
	// au chargement, si le fichier livré la rompait. La vérifier une seconde fois
	// ici mesurerait le même code deux fois.

	// Le point que la validation ne peut pas trancher seule : ce qui construit
	// ou re-tague ne doit pas être enchaîné. Une construction qui s'enchaînerait
	// elle-même bouclerait sans fin, en publiant une image à chaque tour.
	for _, kind := range []string{"build", "retag"} {
		if pipelines.IsVerification(kind) {
			t.Errorf("%s est déclaré vérification : une construction s'enchaînerait elle-même", kind)
		}
	}
}

// Et le pendant : un artefact déclaré doit désigner un paramètre et un nom
// d'objet, sans quoi l'URL est minée sans que personne sache où l'envoyer —
// l'ordonnanceur refuse alors le run, et la release reste en attente d'un test
// qui ne partira pas.
func TestChaqueArtefactDeclareSonParametreEtSonNom(t *testing.T) {
	shipped := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(shipped); err != nil {
		t.Skip("catalogue livré introuvable")
	}

	pipelines, err := catalog.Load(shipped)
	if err != nil {
		t.Fatalf("catalogue livré: %v", err)
	}

	for _, pipeline := range pipelines.Pipelines {
		for name, upload := range pipeline.Uploads {
			if upload.Param == "" || upload.Artifact == "" {
				t.Errorf("%s/%s: param=%q artifact=%q — les deux sont requis",
					pipeline.Kind, name, upload.Param, upload.Artifact)
			}
			// L'extension de l'objet doit dire son contenu : c'est elle qui
			// décide, à la lecture, s'il faut déballer une archive.
			archive := strings.HasSuffix(upload.Artifact, ".tar.gz") ||
				strings.HasSuffix(upload.Artifact, ".tgz")
			if (upload.Format == "archive") != archive {
				t.Errorf("%s/%s: format=%q mais artifact=%q — le lecteur déballera ou non à tort",
					pipeline.Kind, name, upload.Format, upload.Artifact)
			}
		}
	}
}

// Le défaut qui vidait l'écran d'une release : les rapports étaient bien ingérés
// — neuf lignes en base — mais avec `release_id` nul. L'écran les cherche par
// release et n'en trouvait aucun, laissant le score « non évalué » sur une
// release dont les tests avaient tourné et réussi.
//
// La cause tenait en un champ non renseigné à l'appel : `LoadRun` porte bien
// `ReleaseID`, et les trois ingestions le laissaient à zéro.
func TestUnRapportEstRattacheALaReleaseQueSonRunVerifie(t *testing.T) {
	fake := &fakeVerificationStore{
		jobRelease: map[int64]store.JobReleaseContext{
			42: {ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "static-tests"},
		},
	}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	release := sink.releaseOf(context.Background(), tekton.Run{Name: "mse-static-tests-42", JobID: 42})

	if release == nil {
		t.Fatal("aucune release rendue: les rapports s'enregistreraient orphelins")
	}
	if *release != 7 {
		t.Errorf("release %d, attendu 7", *release)
	}
}

// Un run que le dashboard n'a pas lancé — webhook Gitea, CronJob nocturne — ne
// vérifie aucune release. C'est un cas normal : ses rapports restent
// consultables depuis le run, et leur inventer une release les ferait apparaître
// dans le score de quelqu'un d'autre.
func TestUnRunObserveNeRattacheSesRapportsAAucuneRelease(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	if release := sink.releaseOf(context.Background(),
		tekton.Run{Name: "php-static-tests-run-abc", JobID: 0}); release != nil {
		t.Errorf("release %d rendue pour un run observé", *release)
	}
}

// Un job sans release non plus — un lancement manuel depuis la file.
func TestUnLancementManuelNeRattacheRien(t *testing.T) {
	fake := &fakeVerificationStore{jobRelease: map[int64]store.JobReleaseContext{}}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	if release := sink.releaseOf(context.Background(),
		tekton.Run{Name: "mse-static-tests-9", JobID: 9}); release != nil {
		t.Errorf("release %d rendue pour un job sans release", *release)
	}
}

// L'enchaînement automatique et la relance à l'unité portent sur les mêmes
// familles, et le catalogue est désormais leur source **unique**. Ce qui suit ne
// compare donc plus deux listes — il vérifie que le fichier livré désigne bien
// celles qu'on attend, la déclaration étant maintenant ce qu'on peut se tromper
// en écrivant.
func TestLeCatalogueLivreEnchaineLesQuatreFamillesAttendues(t *testing.T) {
	shipped := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(shipped); err != nil {
		t.Skip("catalogue livré introuvable")
	}

	pipelines, err := catalog.Load(shipped)
	if err != nil {
		t.Fatalf("catalogue livré: %v", err)
	}

	want := []string{"security-audit", "static-tests", "unit-tests", "integration-tests"}
	got := pipelines.VerificationKinds()

	if len(got) != len(want) {
		t.Fatalf("familles enchaînées = %v, attendu %v", got, want)
	}
	for index, kind := range want {
		// L'ordre est celui de déclaration, et il devient celui de mise en file :
		// des runs qui changent de rang à chaque construction rendent impossible
		// de comparer deux releases d'un coup d'œil.
		if got[index] != kind {
			t.Errorf("famille %d = %q, attendu %q", index, got[index], kind)
		}
	}
}

/*
 * Une release construit plusieurs images, et le pipeline n'en traite qu'une par
 * exécution : la conclusion est donc appelée une fois par image.
 *
 * Elle ne doit avancer qu'à la dernière. Avancer à la première ferait partir les
 * vérifications sur une release dont deux images sur trois n'existent pas encore
 * au registre — l'audit et l'intégration échoueraient sur un tirage impossible,
 * et l'échec désignerait le test au lieu de la construction.
 */
func TestUneConstructionPartielleNeFaitPasAvancerLaRelease(t *testing.T) {
	fake := &fakeVerificationStore{
		state: "building",
		// Deux images sur trois : la troisième tourne encore.
		builds: store.BuildTally{Pending: 1, Succeeded: 2},
	}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	linked := store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "build",
		State: "building", GitCommit: "abc123",
	}

	if err := sink.advanceRelease(context.Background(), linked, store.JobSucceeded); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	if fake.state != "building" {
		t.Errorf("état %q, attendu building: une image reste à construire", fake.state)
	}
	if len(fake.enqueued) != 0 {
		t.Errorf("%d vérification(s) lancées trop tôt: %+v", len(fake.enqueued), fake.enqueued)
	}

	// La dernière image conclut la série, et c'est elle qui déclenche les tests.
	fake.builds = store.BuildTally{Succeeded: 3}
	if err := sink.advanceRelease(context.Background(), linked, store.JobSucceeded); err != nil {
		t.Fatalf("conclusion finale: %v", err)
	}
	if fake.state != "verifying" {
		t.Errorf("état %q, attendu verifying", fake.state)
	}
	if len(fake.enqueued) == 0 {
		t.Error("aucune vérification lancée après la dernière image")
	}
}

// Un échec conclut tout de suite, sans attendre les autres images. Les faire
// tourner produirait un résultat dont on sait déjà qu'il ne servira pas, et
// retarderait d'autant le moment où l'écran dit ce qui ne va pas.
func TestUneImageEnEchecConclutSansAttendreLesAutres(t *testing.T) {
	fake := &fakeVerificationStore{
		state:  "building",
		builds: store.BuildTally{Pending: 2},
	}
	sink := sinkFor(fake, verificationCatalog())
	sink.releases = fake

	linked := store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", Kind: "build",
		State: "building", GitCommit: "abc123",
	}

	if err := sink.advanceRelease(context.Background(), linked, store.JobFailed); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	if fake.state != "failed" {
		t.Errorf("état %q, attendu failed", fake.state)
	}
	if len(fake.enqueued) != 0 {
		t.Errorf("%d vérification(s) lancées sur une construction en échec", len(fake.enqueued))
	}
}
