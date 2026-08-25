package config

import (
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
)

// setEnv positionne les variables pour la durée du test et restaure l'état
// antérieur, t.Setenv s'en chargeant.
func setEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for k, v := range values {
		t.Setenv(k, v)
	}
}

func validAPIEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":       "postgres://u:p@db:5432/mse",
		"PUBLIC_URL":         "https://dashboard.example.fr",
		"OIDC_ISSUER_URL":    "https://forge.cadoles.com/",
		"OIDC_CLIENT_ID":     "id",
		"OIDC_CLIENT_SECRET": "secret",
		"OIDC_REDIRECT_URL":  "https://dashboard.example.fr/api/v1/auth/callback",
		"BOOTSTRAP_ADMINS":   "vcarroy",
		"MINIO_ENDPOINT":     "nuo-s3.nuonet.fr",
		"MINIO_ACCESS_KEY":   "ak",
		"MINIO_SECRET_KEY":   "sk",
	}
}

func TestLoadAPISucceedsWithCompleteEnvironment(t *testing.T) {
	setEnv(t, validAPIEnv())

	cfg, err := Load(RoleAPI)
	if err != nil {
		t.Fatalf("configuration complète refusée: %v", err)
	}
	if cfg.Storage.Region != "NUO-Region" {
		t.Errorf("région par défaut = %q, attendu NUO-Region: une région erronée invalide la signature SigV4", cfg.Storage.Region)
	}
	if cfg.Storage.Bucket != "mse-dashboard" {
		t.Errorf("bucket par défaut = %q", cfg.Storage.Bucket)
	}
}

// L'erreur doit énumérer tous les manques d'un coup : corriger une configuration
// à raison d'un redémarrage par variable est une perte de temps évitable.
func TestLoadReportsEveryMissingVariableAtOnce(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": ""})

	_, err := Load(RoleAPI)
	if err == nil {
		t.Fatal("une configuration vide doit être refusée")
	}
	for _, expected := range []string{
		"DATABASE_URL", "PUBLIC_URL", "OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID", "BOOTSTRAP_ADMINS", "MINIO_ENDPOINT",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("%s absent du rapport d'erreur", expected)
		}
	}
}

// Sans administrateur d'amorçage, l'authentification marche mais personne ne peut
// rien faire ni débloquer la situation. Le symptôme ne désigne pas sa cause.
func TestLoadRejectsAPIWithoutBootstrapAdmins(t *testing.T) {
	env := validAPIEnv()
	delete(env, "BOOTSTRAP_ADMINS")
	setEnv(t, env)
	t.Setenv("BOOTSTRAP_ADMINS", "")

	_, err := Load(RoleAPI)
	if err == nil || !strings.Contains(err.Error(), "BOOTSTRAP_ADMINS") {
		t.Fatalf("attendu un refus sur BOOTSTRAP_ADMINS, obtenu: %v", err)
	}
}

// L'audit de sécurité dure jusqu'à 90 minutes. Une URL qui expire avant fait
// échouer la publication après que le travail coûteux a été fait.
func TestLoadRejectsPresignTTLShorterThanLongestPipeline(t *testing.T) {
	setEnv(t, validAPIEnv())
	t.Setenv("MINIO_PRESIGN_TTL", "30m")

	_, err := Load(RoleAPI)
	if err == nil || !strings.Contains(err.Error(), "MINIO_PRESIGN_TTL") {
		t.Fatalf("attendu un refus sur MINIO_PRESIGN_TTL, obtenu: %v", err)
	}
}

// `migrate` n'a besoin ni de stockage objet, ni d'OIDC : exiger l'un ou l'autre
// empêcherait d'appliquer un schéma depuis un Job d'initialisation minimal.
func TestLoadMigrateRequiresOnlyDatabase(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/mse"})

	if _, err := Load(RoleMigrate); err != nil {
		t.Fatalf("migrate ne devrait exiger que DATABASE_URL: %v", err)
	}
}

func TestLoadWorkerDoesNotRequireOIDC(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":     "postgres://u:p@db:5432/mse",
		"MINIO_ENDPOINT":   "nuo-s3.nuonet.fr",
		"MINIO_ACCESS_KEY": "ak",
		"MINIO_SECRET_KEY": "sk",
	})

	if _, err := Load(RoleWorker); err != nil {
		t.Fatalf("worker ne devrait pas exiger OIDC: %v", err)
	}
}

func TestLoadRejectsUnknownRole(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/mse"})

	if _, err := Load(Role("frontend")); err == nil {
		t.Fatal("un rôle inconnu doit être refusé")
	}
}

// Un DSN entièrement masqué empêche de diagnostiquer une erreur de connexion —
// précisément la situation où on le journalise.
func TestRedactedKeepsDSNStructureAndHidesPassword(t *testing.T) {
	cfg := Config{
		Database: DatabaseConfig{DSN: "postgres://mse:s3cr3t@db.example.fr:5432/mse?sslmode=require"},
		Storage:  StorageConfig{SecretKey: "sk"},
		OIDC:     OIDCConfig{ClientSecret: "cs"},
	}

	got := cfg.Redacted()

	if strings.Contains(got.Database.DSN, "s3cr3t") {
		t.Error("le mot de passe fuit dans le DSN masqué")
	}
	for _, keep := range []string{"db.example.fr:5432", "/mse", "sslmode=require", "mse:"} {
		if !strings.Contains(got.Database.DSN, keep) {
			t.Errorf("le DSN masqué a perdu %q, il n'est plus diagnosticable: %s", keep, got.Database.DSN)
		}
	}
	if got.Storage.SecretKey == "sk" || got.OIDC.ClientSecret == "cs" {
		t.Error("un secret n'a pas été masqué")
	}
}

// Un secret absent doit rester visiblement absent : le masquer en "<masqué>"
// ferait croire à une valeur configurée.
func TestRedactedDistinguishesEmptyFromHidden(t *testing.T) {
	got := Config{}.Redacted()

	if got.Storage.SecretKey != "" || got.OIDC.ClientSecret != "" || got.Database.DSN != "" {
		t.Error("un secret absent ne doit pas ressembler à un secret masqué")
	}
}

func TestListOrIgnoresEmptyEntries(t *testing.T) {
	t.Setenv("BOOTSTRAP_ADMINS", " alice , , bob ")

	got := listOr("BOOTSTRAP_ADMINS", nil)

	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("découpage incorrect: %#v", got)
	}
}

func TestDurationOrFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("SESSION_TTL", "pas-une-durée")

	if got := durationOr("SESSION_TTL", 12*time.Hour); got != 12*time.Hour {
		t.Errorf("attendu le repli 12h, obtenu %s", got)
	}
}

// L'espace final d'un préfixe est significatif : `MSE ` et `MSE` ne désignent pas
// la même règle, et le second rattacherait `MSE3-1.5` à un numéro inventé.
func TestLesPrefixesDeVersionGardentLeurEspaceFinal(t *testing.T) {
	setEnv(t, validAPIEnv())
	t.Setenv("VERSION_NAME_PREFIXES", "v, MSE ")

	cfg, err := Load(RoleAPI)
	if err != nil {
		t.Fatalf("configuration refusée: %v", err)
	}
	prefixes := cfg.Versions.NamePrefixes
	if len(prefixes) != 2 || prefixes[0] != "v" || prefixes[1] != "MSE " {
		t.Fatalf("préfixes = %q", prefixes)
	}

	names := release.NewNormalizer(prefixes)
	if got := names.Normalize("MSE 3.1.0"); got != "3.1.0" {
		t.Errorf("`MSE 3.1.0` normalisé en %q", got)
	}
	// La convention historique reste intacte : « MSE3, version 1.5 » se lirait
	// 3.1.5 ou 1.5 selon l'intention, et rien dans les données ne tranche.
	if got := names.Normalize("MSE3-1.5"); got != "MSE3-1.5" {
		t.Errorf("`MSE3-1.5` ne doit pas être deviné, obtenu %q", got)
	}
}

// Sans déclaration, seul `v` s'applique : il vient du versionnement sémantique et
// ne suppose rien d'un projet donné.
func TestSansDeclarationSeulVEstRetire(t *testing.T) {
	setEnv(t, validAPIEnv())

	cfg, err := Load(RoleAPI)
	if err != nil {
		t.Fatalf("configuration refusée: %v", err)
	}
	if prefixes := cfg.Versions.NamePrefixes; len(prefixes) != 1 || prefixes[0] != "v" {
		t.Errorf("préfixes par défaut = %q", prefixes)
	}
}
