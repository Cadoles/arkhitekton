package worker

import (
	"context"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakePresigner struct {
	signed map[string]string
	err    error
}

func (f fakePresigner) PresignPut(_ context.Context, key string, _ time.Duration) (*url.URL, error) {
	if f.err != nil {
		return nil, f.err
	}
	signed := "https://s3.example/" + key + "?X-Amz-Signature=abc"
	f.signed[key] = signed
	return url.Parse(signed)
}

func uploadPipeline() catalog.Pipeline {
	return catalog.Pipeline{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]catalog.Upload{
			"results": {Param: "results-upload-url", Artifact: "static-tests.tar.gz", Family: "static"},
		},
	}
}

// La clé est calculée avant la création du run : c'est ce qui permet au
// dashboard de la retrouver ensuite sans balayer un préfixe (B14).
func TestUploadURLsAreMintedBeforeTheRun(t *testing.T) {
	presigner := fakePresigner{signed: map[string]string{}}
	scheduler := &Scheduler{uploads: presigner, logger: slog.New(slog.DiscardHandler)}

	params := map[string]string{}
	if err := scheduler.addUploadURLs(context.Background(), uploadPipeline(), "static-tests-42", params); err != nil {
		t.Fatalf("URLs: %v", err)
	}

	const key = "raw/static-tests-42/static-tests.tar.gz"
	if _, signed := presigner.signed[key]; !signed {
		t.Errorf("clés signées = %v, attendu %q", presigner.signed, key)
	}
	if params["results-upload-url"] != presigner.signed[key] {
		t.Errorf("paramètre = %q", params["results-upload-url"])
	}
}

// Sans stockage, le run part quand même : le refuser priverait l'utilisateur
// d'une exécution qui reste utile, ses journaux passant par un autre chemin.
func TestWithoutStorageTheRunStillStarts(t *testing.T) {
	scheduler := &Scheduler{logger: slog.New(slog.DiscardHandler)}

	params := map[string]string{}
	if err := scheduler.addUploadURLs(context.Background(), uploadPipeline(), "static-tests-42", params); err != nil {
		t.Fatalf("erreur = %v, attendu aucune", err)
	}
	if len(params) != 0 {
		t.Errorf("paramètres = %v, attendu aucun", params)
	}
}

func TestAnIncompleteUploadDeclarationIsRefused(t *testing.T) {
	scheduler := &Scheduler{
		uploads: fakePresigner{signed: map[string]string{}}, logger: slog.New(slog.DiscardHandler),
	}
	pipeline := catalog.Pipeline{
		Kind:    "static-tests",
		Uploads: map[string]catalog.Upload{"results": {Artifact: "x.tar.gz"}},
	}

	if err := scheduler.addUploadURLs(context.Background(), pipeline, "run-1", map[string]string{}); err == nil {
		t.Error("un artefact sans paramètre cible doit être refusé")
	}
}

// Un tag de release compose des clés ; un nom de run aussi. La validation des
// segments vaut ici comme ailleurs.
func TestAnUnsafeRunNameIsRefused(t *testing.T) {
	scheduler := &Scheduler{
		uploads: fakePresigner{signed: map[string]string{}}, logger: slog.New(slog.DiscardHandler),
	}

	if err := scheduler.addUploadURLs(
		context.Background(), uploadPipeline(), "../evasion", map[string]string{}); err == nil {
		t.Error("un nom de run hors grammaire doit être refusé")
	}
}

// Le défaut qui a coûté trois builds fantômes : les identifiants de jobs
// repartent à 1 sur une base neuve, alors que les PipelineRun survivent dans le
// cluster. Sans identité de base, le nouveau job portait le nom d'un run
// existant, la création se heurtait à un conflit lu comme « c'était déjà fait »,
// et rien ne partait — l'interface affichant l'échec d'une exécution vieille de
// plusieurs heures.
func TestLeNomDunRunDistingueDeuxGenerationsDeBase(t *testing.T) {
	ancienne := RunName("a1b2c3", "build", 1)
	nouvelle := RunName("d4e5f6", "build", 1)

	if ancienne == nouvelle {
		t.Errorf("même nom pour deux bases: %q — une remise à zéro produirait des homonymes", ancienne)
	}
}

// La propriété que le nommage doit conserver : deux essais du même job donnent le
// même nom. C'est elle qui rend la reprise sûre — une seconde création se heurte
// à un conflit que l'ordonnanceur lit comme « c'était déjà fait » au lieu de
// créer un doublon.
func TestDeuxEssaisDuMemeJobDonnentLeMemeNom(t *testing.T) {
	if RunName("a1b2c3", "build", 7) != RunName("a1b2c3", "build", 7) {
		t.Error("le nom doit rester déterministe pour un job donné")
	}
	// Et deux jobs distincts ne se confondent pas.
	if RunName("a1b2c3", "build", 7) == RunName("a1b2c3", "build", 8) {
		t.Error("deux jobs distincts ne doivent pas partager un nom")
	}
	if RunName("a1b2c3", "build", 7) == RunName("a1b2c3", "static-tests", 7) {
		t.Error("deux natures distinctes ne doivent pas partager un nom")
	}
}

// Le nom doit rester un nom de ressource Kubernetes valide.
func TestLeNomResteUnNomKubernetesValide(t *testing.T) {
	name := RunName("a1b2c3", "security-audit", 12345)

	if len(name) > 63 {
		t.Errorf("nom trop long (%d): %q", len(name), name)
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Errorf("caractère invalide %q dans %q", r, name)
		}
	}
}

// Sans identité, l'ancien nommage subsiste : les tests n'ont pas à la fournir, et
// un déploiement qui n'a jamais tourné ne change pas de comportement.
func TestSansIdentiteLAncienNommageSubsiste(t *testing.T) {
	if got := RunName("", "build", 3); got != "mse-build-3" {
		t.Errorf("nom sans identité: %q", got)
	}
}

// Les paramètres que le catalogue ne déclare pas passent tels quels : c'est ce
// qui rend possible une exécution personnalisée, où l'on renseigne un paramètre
// que le pipeline connaît mais que le dashboard n'a pas décrit.
func TestUnParametreNonDeclarePasseTelQuel(t *testing.T) {
	scheduler := &Scheduler{config: map[string]string{"forge.mse.repoUrl": "ssh://git@forge/CNOUS/mse"}}

	pipeline := catalog.Pipeline{
		Kind: "unit-tests",
		Params: map[string]catalog.Param{
			"revision": {Source: catalog.SourceInput, Required: true},
			"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
		},
	}

	params, err := scheduler.resolveParams(pipeline, map[string]string{
		"revision": "unstable",
		"debug":    "true",
	}, scheduler.config)
	if err != nil {
		t.Fatalf("résolution: %v", err)
	}
	if params["debug"] != "true" {
		t.Errorf("paramètre supplémentaire perdu: %v", params)
	}
	if params["revision"] != "unstable" || params["repo-url"] != "ssh://git@forge/CNOUS/mse" {
		t.Errorf("paramètres déclarés = %v", params)
	}
}

// Le passe-plat n'affaiblit pas la séparation des sources. Laisser une demande
// fournir un paramètre de source `config` permettrait de pointer un pipeline
// vers un autre dépôt que celui prévu — exactement ce que cette séparation
// existe pour empêcher.
func TestUneDemandeNeRemplacePasUnParametreDeConfiguration(t *testing.T) {
	scheduler := &Scheduler{config: map[string]string{"forge.mse.repoUrl": "ssh://git@forge/CNOUS/mse"}}

	pipeline := catalog.Pipeline{
		Kind: "unit-tests",
		Params: map[string]catalog.Param{
			"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
			"image":    {Source: catalog.SourceLiteral, Value: "reg.example.fr/mse/ci"},
		},
	}

	params, err := scheduler.resolveParams(pipeline, map[string]string{
		"repo-url": "ssh://git@ailleurs/attaquant/mse",
		"image":    "docker.io/attaquant/ci",
	}, scheduler.config)
	if err != nil {
		t.Fatalf("résolution: %v", err)
	}
	if params["repo-url"] != "ssh://git@forge/CNOUS/mse" {
		t.Errorf("dépôt = %q: une demande a écrasé la configuration", params["repo-url"])
	}
	if params["image"] != "reg.example.fr/mse/ci" {
		t.Errorf("image = %q: une demande a écrasé une valeur littérale", params["image"])
	}
}

// Un réglage de projet — la kustomisation que le plugin MSE fournit — remplace
// celui de l'instance pour ce job, et pour lui seul (doc 16 §16.18).
func TestUnReglageDeProjetRemplaceCeluiDeLInstance(t *testing.T) {
	scheduler := (&Scheduler{config: map[string]string{
		"forge.mseKustom.repoUrl": "ssh://git@forge/CNOUS/mse-kustom",
		"registry.project":        "reg.example.fr/cnous",
	}}).WithProjectSettings(func(_ context.Context, projectID int64) (map[string]string, error) {
		if projectID != 7 {
			return nil, nil
		}
		return map[string]string{"forge.mseKustom.repoUrl": "ssh://git@forge/vcarroy/kustom"}, nil
	})

	projet := int64(7)
	settings, err := scheduler.settingsFor(context.Background(), store.Job{ID: 1, ProjectID: &projet})
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}
	if settings["forge.mseKustom.repoUrl"] != "ssh://git@forge/vcarroy/kustom" {
		t.Errorf("kustomisation = %q, attendu celle du projet", settings["forge.mseKustom.repoUrl"])
	}
	// Ce que le projet ne dit pas reste celui de l'instance.
	if settings["registry.project"] != "reg.example.fr/cnous" {
		t.Errorf("registre = %q", settings["registry.project"])
	}
	// Et la carte de l'instance n'a pas bougé : elle est partagée par tous les jobs.
	if scheduler.config["forge.mseKustom.repoUrl"] != "ssh://git@forge/CNOUS/mse-kustom" {
		t.Error("la configuration de l'instance a été écrite")
	}

	autre := int64(9)
	settings, err = scheduler.settingsFor(context.Background(), store.Job{ID: 2, ProjectID: &autre})
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}
	if settings["forge.mseKustom.repoUrl"] != "ssh://git@forge/CNOUS/mse-kustom" {
		t.Errorf("un autre projet a hérité du réglage: %q", settings["forge.mseKustom.repoUrl"])
	}
}

type depotsDeProjets map[int64]store.Project

func (d depotsDeProjets) GetProject(_ context.Context, id int64) (store.Project, error) {
	project, found := d[id]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return project, nil
}

// Les pipelines d'un projet clonent **son** dépôt, déclaré en base. Sans cela, un
// second projet construirait le dépôt du premier (doc 16 §16.24).
func TestLesPipelinesDUnProjetClonentSonDepot(t *testing.T) {
	scheduler := (&Scheduler{config: map[string]string{
		projectRepoKey: "ssh://git@forge/CNOUS/mse.git",
		legacyRepoKey:  "ssh://git@forge/CNOUS/mse.git",
	}}).WithProjectRepositories(depotsDeProjets{
		7: {ID: 7, Repository: "vcarroy/themes", ForgeURL: "https://forge.example.fr"},
		// Déclaré avant que sa forge ne soit renseignée.
		8: {ID: 8, Repository: "vcarroy/sans-forge"},
	}, "2222")

	projet := int64(7)
	settings, err := scheduler.settingsFor(context.Background(), store.Job{ID: 1, ProjectID: &projet})
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}
	const attendu = "ssh://git@forge.example.fr:2222/vcarroy/themes.git"
	if settings[projectRepoKey] != attendu {
		t.Errorf("dépôt = %q, attendu celui du projet", settings[projectRepoKey])
	}
	// L'ancienne clé porte la même valeur, tant qu'un catalogue la nomme.
	if settings[legacyRepoKey] != attendu {
		t.Errorf("ancienne clé = %q", settings[legacyRepoKey])
	}

	sansForge := int64(8)
	settings, err = scheduler.settingsFor(context.Background(), store.Job{ID: 2, ProjectID: &sansForge})
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}
	if settings[projectRepoKey] != "ssh://git@forge/CNOUS/mse.git" {
		t.Errorf("dépôt = %q, attendu celui de l'instance", settings[projectRepoKey])
	}

	// Un job sans projet garde celui de l'instance.
	settings, err = scheduler.settingsFor(context.Background(), store.Job{ID: 3})
	if err != nil || settings[projectRepoKey] != "ssh://git@forge/CNOUS/mse.git" {
		t.Errorf("job sans projet: %q (%v)", settings[projectRepoKey], err)
	}
}
