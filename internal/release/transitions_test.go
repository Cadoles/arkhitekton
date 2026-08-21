package release

import "testing"

// Les deux chemins de doc 04 §4.2, parcourus de bout en bout.
//
// Le chemin dérivé ne passe ni par `building` ni par `verifying` : rien n'est
// construit, donc rien ne s'exécute (B17). Modéliser un `building` fictif ferait
// attendre l'interface sur un pipeline qui ne viendra jamais.
func TestLesDeuxCheminsDocumentesSontFranchissables(t *testing.T) {
	chemins := map[string][]State{
		"construite": {StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased},
		"dérivée":    {StateDraft, StateReady, StateReleased},
	}

	for nom, chemin := range chemins {
		for i := 0; i < len(chemin)-1; i++ {
			from, to := chemin[i], chemin[i+1]
			if !CanTransition(from, to) {
				t.Errorf("chemin %s: %s → %s refusé", nom, from, to)
			}
		}
	}
}

// Une release livrée ne redescend pas vers les tests : rejouer sa validation
// après coup réécrirait l'histoire d'un livrable déjà parti chez le client.
func TestUneReleaseLivreeNeRedescendPas(t *testing.T) {
	for _, to := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateFailed} {
		if CanTransition(StateReleased, to) {
			t.Errorf("released → %s devrait être refusé", to)
		}
	}
	// Elle peut en revanche être abandonnée : une régression découverte après
	// livraison doit pouvoir se constater.
	if !CanTransition(StateReleased, StateCancelled) {
		t.Error("released → cancelled devrait être permis")
	}
}

// `cancelled` est terminal à dessein : reprendre une release abandonnée
// masquerait la raison de son abandon, alors qu'en créer une nouvelle la laisse
// lisible.
func TestUneReleaseAbandonneeEstTerminale(t *testing.T) {
	if !StateCancelled.Terminal() {
		t.Error("cancelled devrait être terminal")
	}
	for _, to := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased, StateFailed} {
		if CanTransition(StateCancelled, to) {
			t.Errorf("cancelled → %s devrait être refusé", to)
		}
	}
	// Aucun autre état n'est terminal : tous doivent pouvoir au moins être
	// abandonnés, sans quoi une release resterait coincée sans issue.
	for _, state := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased, StateFailed} {
		if state.Terminal() {
			t.Errorf("%s ne devrait pas être terminal", state)
		}
	}
}

// Tout état non terminal doit pouvoir être abandonné : c'est la porte de sortie
// d'une release qu'on renonce à livrer, et son absence laisserait des lignes
// vivantes que rien ne peut clore.
func TestToutEtatVivantPeutEtreAbandonne(t *testing.T) {
	for _, state := range []State{
		StateDraft, StateBuilding, StateVerifying, StateReady, StateReleased, StateFailed,
	} {
		if !CanTransition(state, StateCancelled) {
			t.Errorf("%s → cancelled devrait être permis", state)
		}
	}
}

// Un échec est relançable dans les deux sens : `verifying` quand les images
// existent déjà, `building` quand il faut reconstruire.
func TestUnEchecEstRelancable(t *testing.T) {
	if !CanTransition(StateFailed, StateBuilding) {
		t.Error("failed → building devrait être permis")
	}
	if !CanTransition(StateFailed, StateVerifying) {
		t.Error("failed → verifying devrait être permis")
	}
	// Mais un échec ne devient pas livrable sans décision humaine.
	if CanTransition(StateFailed, StateReleased) {
		t.Error("failed → released devrait être refusé")
	}
}

// `failed → ready` est permis, et cette règle a changé.
//
// Elle était refusée au motif qu'« il faut repasser par les tests ». C'était
// précisément le cas qu'elle interdisait : relancer une famille de vérification
// **est** repasser par les tests, et son verdict remplace le précédent. La
// release restait donc en échec pour un résultat périmé, et le bouton
// « Rejouer » ne rattrapait rien. Constaté sur 3.2.0-rc3, dont les trois
// familles étaient vertes au dernier essai.
//
// Ce que la règle protégeait est ailleurs, et tient toujours : `failed` ne mène
// pas à `released`. Passer en `ready` ne livre rien — c'est l'état « décision en
// attente », et la décision reste humaine.
func TestUnEchecRattrapeParUneRelanceRedevientDecidable(t *testing.T) {
	if !CanTransition(StateFailed, StateReady) {
		t.Error("failed → ready devrait être permis: une relance réussie remplace le verdict")
	}
}

// Aucun état ne se transite vers lui-même : une transition sans effet masquerait
// un appelant qui croit faire avancer quelque chose.
func TestAucunEtatNeSeTransiteVersLuiMeme(t *testing.T) {
	for _, state := range []State{
		StateDraft, StateBuilding, StateVerifying, StateReady,
		StateReleased, StateFailed, StateCancelled,
	} {
		if CanTransition(state, state) {
			t.Errorf("%s → %s devrait être refusé", state, state)
		}
	}
}

// Un état inconnu n'ouvre rien : une valeur lue en base qui ne correspond à aucun
// état déclaré ne doit pas se comporter comme un joker.
func TestUnEtatInconnuNouvreRien(t *testing.T) {
	inconnu := State("inventé")
	for _, to := range []State{StateBuilding, StateReady, StateReleased, StateCancelled} {
		if CanTransition(inconnu, to) {
			t.Errorf("inventé → %s devrait être refusé", to)
		}
	}
	// Et il est vu comme terminal, ce qui est le comportement sûr : rien ne
	// l'avancera tant que personne n'aura corrigé la donnée.
	if !inconnu.Terminal() {
		t.Error("un état inconnu devrait être vu comme terminal")
	}
}

// La construction ne saute pas les tests dans le chemin construit, mais
// `building → ready` reste ouvert : toutes les familles de tests ne sont pas
// obligatoires, et une release dont aucune ne l'est passe directement en
// attente de décision.
func TestLaConstructionPeutConclureSansTests(t *testing.T) {
	if !CanTransition(StateBuilding, StateReady) {
		t.Error("building → ready devrait rester permis")
	}
	if !CanTransition(StateBuilding, StateVerifying) {
		t.Error("building → verifying devrait être permis")
	}
}
