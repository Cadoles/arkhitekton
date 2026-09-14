package flux

import (
	"strings"
	"testing"
)

func targets() (unstable, qualif, prod PromotionTarget) {
	return PromotionTarget{ID: "unstable", Suffix: "unstable", AcceptsPrerelease: true},
		// Le piège de B7 : Flux désigne la qualification sous son ancien nom.
		PromotionTarget{ID: "qualif", Suffix: "testing", AcceptsPrerelease: true},
		PromotionTarget{ID: "prod", Suffix: "prod", AcceptsPrerelease: false}
}

func TestPromotionTagUsesTheFluxSuffixNotTheEnvironmentName(t *testing.T) {
	_, qualif, _ := targets()

	tag, err := PromotionTag("1.8.0-rc1", qualif, "")
	if err != nil {
		t.Fatalf("promotion refusée: %v", err)
	}
	if tag != "1.8.0-rc1_testing" {
		t.Errorf("tag = %q, attendu 1.8.0-rc1_testing", tag)
	}
}

// Le refus qui compte le plus : pousser un tag qu'aucune ImagePolicy ne retient
// ne produit aucune erreur. Le registre l'accepte, la task réussit, rien ne se
// déploie — un échec qui ressemble à un succès.
func TestPromotionRefusesPrereleaseWhereFluxWouldIgnoreIt(t *testing.T) {
	_, _, prod := targets()

	_, err := PromotionTag("1.8.0-rc1", prod, "")
	if err == nil {
		t.Fatal("une pré-release vers la production doit être refusée")
	}
	for _, expected := range []string{"prod", "1.8.0-rc1", "ignorerait"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("le message ne contient pas %q: %v", expected, err)
		}
	}
}

func TestPromotionAcceptsFinalReleaseInProduction(t *testing.T) {
	_, _, prod := targets()

	tag, err := PromotionTag("1.8.0", prod, "")
	if err != nil {
		t.Fatalf("promotion refusée: %v", err)
	}
	if tag != "1.8.0_prod" {
		t.Errorf("tag = %q", tag)
	}
}

// Le séparateur reste un paramètre : `+` est exclu de la grammaire des tags OCI,
// et si la convention évolue le gabarit suit sans changement de code.
func TestPromotionTagHonoursTheTemplate(t *testing.T) {
	unstable, _, _ := targets()

	tag, err := PromotionTag("3.1.0", unstable, "{suffix}-{version}")
	if err != nil {
		t.Fatalf("gabarit refusé: %v", err)
	}
	if tag != "unstable-3.1.0" {
		t.Errorf("tag = %q", tag)
	}
}

// Un gabarit qui produirait un tag hors grammaire OCI doit être refusé ici : le
// registre le refuserait aussi, mais plus tard et avec un message qui parle de
// manifeste.
func TestPromotionTagRejectsInvalidOCITags(t *testing.T) {
	unstable, _, _ := targets()

	if _, err := PromotionTag("3.1.0", unstable, "{version}+{suffix}"); err == nil {
		t.Error("un tag portant un + doit être refusé")
	}
	if _, err := PromotionTag("3.1.0", unstable, "{version}/{suffix}"); err == nil {
		t.Error("un tag portant une barre oblique doit être refusé")
	}
}

func TestPromotionRefusesUnusableInputs(t *testing.T) {
	unstable, _, _ := targets()

	if _, err := PromotionTag("", unstable, ""); err == nil {
		t.Error("une version vide doit être refusée")
	}
	if _, err := PromotionTag("1.8.0", PromotionTarget{ID: "sans-suffixe"}, ""); err == nil {
		t.Error("un environnement sans suffixe doit être refusé")
	}
}

// L'ancien schéma de tag mktools **est** du SemVer valide : `2026.8.3` avec la
// pré-release `k8sunstable.1249.520c9a020`. Cette fonction l'accepte donc, et
// c'est correct — elle ne juge que la forme.
//
// Le test existe pour fixer la limite de sa responsabilité. Le garde-fou contre
// ce schéma est ailleurs, à la création de release, où le tag est confronté au
// nom de version Redmine. Et la conséquence, si un tel tag était promu, est
// documentée en B7 : son majeur étant une année, il primerait sur toute version
// SemVer sur les environnements qui acceptent les pré-releases.
func TestPromotionTagJudgesFormOnly(t *testing.T) {
	unstable, _, _ := targets()

	tag, err := PromotionTag("2026.8.3-k8sunstable.1249.520c9a020", unstable, "")
	if err != nil {
		t.Fatalf("un tag SemVer valide ne doit pas être refusé ici: %v", err)
	}
	if tag != "2026.8.3-k8sunstable.1249.520c9a020_unstable" {
		t.Errorf("tag = %q", tag)
	}
}

// Promouvoir n'est pas reconstruire : le plan porte l'ancien et le nouveau tag,
// et le digest reste le même.
func TestPlanPromotionCarriesBothTags(t *testing.T) {
	_, qualif, _ := targets()

	plan, err := PlanPromotion("1.8.0-rc3", "1.8.0-rc3", qualif, "")
	if err != nil {
		t.Fatalf("plan refusé: %v", err)
	}
	if plan.OldTag != "1.8.0-rc3" || plan.NewTag != "1.8.0-rc3_testing" {
		t.Errorf("plan = %+v", plan)
	}
	if plan.EnvironmentID != "qualif" {
		t.Errorf("environnement = %q", plan.EnvironmentID)
	}
}

// Le cas qui justifie les deux paramètres : une release finale produite par
// re-tag. Son image porte encore la version de la RC, et composer le tag de
// promotion depuis cette version lui interdirait la production — la plage SemVer
// de la production excluant les pré-releases (B7, B17).
func TestPlanPromotionComposesFromTheReleaseTagAndRetagsFromTheImage(t *testing.T) {
	prod := PromotionTarget{ID: "prod", Suffix: "prod", AcceptsPrerelease: false}

	plan, err := PlanPromotion("1.8.0", "1.8.0-rc3", prod, "")
	if err != nil {
		t.Fatalf("une release finale doit pouvoir atteindre la production: %v", err)
	}
	if plan.NewTag != "1.8.0_prod" {
		t.Errorf("nouveau tag = %q, attendu 1.8.0_prod", plan.NewTag)
	}
	// Le re-tag part de ce qui existe dans le registre, et c'est l'image de la RC.
	if plan.OldTag != "1.8.0-rc3" {
		t.Errorf("ancien tag = %q, attendu celui de l'image construite", plan.OldTag)
	}
}

func TestPlanPromotionRefusesAReleaseWithoutImage(t *testing.T) {
	if _, err := PlanPromotion("1.8.0", "", PromotionTarget{ID: "unstable", Suffix: "unstable"}, ""); err == nil {
		t.Error("une release sans image ne peut pas être promue")
	}
}
