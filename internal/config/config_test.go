package config

import (
	"strings"
	"testing"
	"time"
)

// setEnv sets variables for the duration of the test; t.Setenv restores them.
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
		t.Fatalf("complete configuration refused: %v", err)
	}
	if cfg.Storage.Region != "NUO-Region" {
		t.Errorf("default region = %q, expected NUO-Region: a wrong region invalidates the SigV4 signature", cfg.Storage.Region)
	}
	if cfg.Storage.Bucket != "arkhitekton" {
		t.Errorf("default bucket = %q", cfg.Storage.Bucket)
	}
}

// The error must list every missing variable at once: fixing a configuration at
// one restart per variable is avoidable waste.
func TestLoadReportsEveryMissingVariableAtOnce(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": ""})

	_, err := Load(RoleAPI)
	if err == nil {
		t.Fatal("an empty configuration must be refused")
	}
	for _, expected := range []string{
		"DATABASE_URL", "PUBLIC_URL", "OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID", "BOOTSTRAP_ADMINS", "MINIO_ENDPOINT",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("%s missing from the error report", expected)
		}
	}
}

// Without a bootstrap administrator, authentication works but nobody can do
// anything or unblock it. The symptom does not point at its cause.
func TestLoadRejectsAPIWithoutBootstrapAdmins(t *testing.T) {
	env := validAPIEnv()
	delete(env, "BOOTSTRAP_ADMINS")
	setEnv(t, env)
	t.Setenv("BOOTSTRAP_ADMINS", "")

	_, err := Load(RoleAPI)
	if err == nil || !strings.Contains(err.Error(), "BOOTSTRAP_ADMINS") {
		t.Fatalf("expected a refusal on BOOTSTRAP_ADMINS, got: %v", err)
	}
}

// The longest pipeline runs for well over an hour. A URL that expires before it
// ends fails the upload after the expensive work is done.
func TestLoadRejectsPresignTTLShorterThanLongestPipeline(t *testing.T) {
	setEnv(t, validAPIEnv())
	t.Setenv("MINIO_PRESIGN_TTL", "30m")

	_, err := Load(RoleAPI)
	if err == nil || !strings.Contains(err.Error(), "MINIO_PRESIGN_TTL") {
		t.Fatalf("expected a refusal on MINIO_PRESIGN_TTL, got: %v", err)
	}
}

// `migrate` needs neither object store nor OIDC: requiring either would stop a
// minimal init Job from applying the schema.
func TestLoadMigrateRequiresOnlyDatabase(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/mse"})

	if _, err := Load(RoleMigrate); err != nil {
		t.Fatalf("migrate should require only DATABASE_URL: %v", err)
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
		t.Fatalf("worker should not require OIDC: %v", err)
	}
}

func TestLoadRejectsUnknownRole(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/mse"})

	if _, err := Load(Role("frontend")); err == nil {
		t.Fatal("an unknown role must be refused")
	}
}

// A fully masked DSN cannot be used to diagnose a connection error — precisely
// the situation in which it is logged.
func TestRedactedKeepsDSNStructureAndHidesPassword(t *testing.T) {
	cfg := Config{
		Database: DatabaseConfig{DSN: "postgres://mse:s3cr3t@db.example.fr:5432/mse?sslmode=require"},
		Storage:  StorageConfig{SecretKey: "sk"},
		OIDC:     OIDCConfig{ClientSecret: "cs"},
	}

	got := cfg.Redacted()

	if strings.Contains(got.Database.DSN, "s3cr3t") {
		t.Error("the password leaks into the masked DSN")
	}
	for _, keep := range []string{"db.example.fr:5432", "/mse", "sslmode=require", "mse:"} {
		if !strings.Contains(got.Database.DSN, keep) {
			t.Errorf("the masked DSN lost %q, it can no longer be diagnosed: %s", keep, got.Database.DSN)
		}
	}
	if got.Storage.SecretKey == "sk" || got.OIDC.ClientSecret == "cs" {
		t.Error("a secret was not masked")
	}
}

// A missing secret must stay visibly missing: masking it would suggest a value is
// configured.
func TestRedactedDistinguishesEmptyFromHidden(t *testing.T) {
	got := Config{}.Redacted()

	if got.Storage.SecretKey != "" || got.OIDC.ClientSecret != "" || got.Database.DSN != "" {
		t.Error("a missing secret must not look like a masked one")
	}
}

func TestListOrIgnoresEmptyEntries(t *testing.T) {
	t.Setenv("BOOTSTRAP_ADMINS", " alice , , bob ")

	got := listOr("BOOTSTRAP_ADMINS", nil)

	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("wrong split: %#v", got)
	}
}

func TestDurationOrFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("SESSION_TTL", "not-a-duration")

	if got := durationOr("SESSION_TTL", 12*time.Hour); got != 12*time.Hour {
		t.Errorf("expected the 12h fallback, got %s", got)
	}
}

// A rename must surface at start-up, not in use.
//
// A leftover variable would produce no error: the forge would be "unconfigured", a
// perfectly valid state where the form falls back to free-text input, and the
// tracker would simply stop synchronising. Two silent degradations nobody would
// connect to the rename.
func TestUneVariableRenommeeEmpecheLeDemarrage(t *testing.T) {
	env := validAPIEnv()
	env["GITEA_TOKEN"] = "token"
	setEnv(t, env)

	_, err := Load(RoleAPI)
	if err == nil {
		t.Fatal("a removed variable must prevent start-up")
	}
	// The message must name the replacement: saying only "unknown" would force
	// reading the source to find out what to use instead.
	if !strings.Contains(err.Error(), "GITEA_TOKEN") ||
		!strings.Contains(err.Error(), "FORGE_TOKEN") {
		t.Errorf("message %q — it must name the old one and the new one", err)
	}
}

// All reported at once: discovering mistakes one by one costs one restart each.
func TestToutesLesVariablesRenommeesSontSignaleesEnsemble(t *testing.T) {
	env := validAPIEnv()
	env["GITEA_URL"] = "https://forge.example"
	env["REDMINE_API_KEY"] = "clef"
	setEnv(t, env)

	_, err := Load(RoleAPI)
	if err == nil {
		t.Fatal("two removed variables must prevent start-up")
	}
	for _, name := range []string{"GITEA_URL", "FORGE_URL", "REDMINE_API_KEY", "TRACKER_API_KEY"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("message %q — %s manque", err, name)
		}
	}
}

// An empty leftover variable still counts: its presence in an environment file
// says someone still believes they configure something with it.
func TestUneVariableRenommeeVideCompteAussi(t *testing.T) {
	env := validAPIEnv()
	env["REDMINE_PROJECT"] = ""
	setEnv(t, env)

	if _, err := Load(RoleAPI); err == nil {
		t.Error("a removed variable, even empty, must prevent start-up")
	}
}

// The current names are accepted without reservation.
func TestLeNouveauVocabulaireEstAccepte(t *testing.T) {
	env := validAPIEnv()
	env["FORGE_URL"] = "https://forge.example"
	env["FORGE_TOKEN"] = "token"
	env["FORGE_REPO"] = "CNOUS/mse"
	env["TRACKER_URL"] = "https://suivi.example"
	env["TRACKER_API_KEY"] = "clef"
	setEnv(t, env)

	cfg, err := Load(RoleAPI)
	if err != nil {
		t.Fatalf("configuration refused: %v", err)
	}
	if !cfg.Forge.Configured() {
		t.Error("the forge should be configured")
	}
	if cfg.Tracker.URL != "https://suivi.example" {
		t.Errorf("tracker URL = %q", cfg.Tracker.URL)
	}
}
