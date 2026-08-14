package results

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

/*
Analyse statique — PHPStan et PHP-CS-Fixer.

Ces outils ne produisent pas des « tests » mais des **violations**. Les projeter
malgré tout en cas de test n'est pas un artifice : la question posée est la même —
qu'est-ce qui est apparu, qu'est-ce qui a disparu — et un modèle unique permet de
la poser une seule fois, dans une seule requête, pour toutes les familles.

L'identité d'une violation est son **fichier + sa règle**, jamais son numéro de
ligne. Une ligne bouge à chaque commit : l'y attacher signalerait une violation
résolue et une nouvelle apparue à chaque fois qu'on insère une ligne au-dessus.
*/

type phpstanReport struct {
	Totals struct {
		Errors     int `json:"errors"`
		FileErrors int `json:"file_errors"`
	} `json:"totals"`
	// Files est indexé par chemin. `json:"files"` peut valoir `[]` quand il n'y a
	// aucune erreur — PHP sérialise un tableau vide là où on attend un objet —, ce
	// que `json.RawMessage` absorbe.
	Files  json.RawMessage `json:"files"`
	Errors []string        `json:"errors"`
}

type phpstanFile struct {
	Errors   int `json:"errors"`
	Messages []struct {
		Message    string `json:"message"`
		Line       int    `json:"line"`
		Identifier string `json:"identifier"`
		Ignorable  bool   `json:"ignorable"`
	} `json:"messages"`
}

// ParsePHPStan lit la sortie JSON de PHPStan.
func ParsePHPStan(raw []byte) (Report, error) {
	report := Report{Family: FamilyStatic, Tool: "phpstan"}

	var source phpstanReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return report, ErrUnsupported{Tool: "phpstan", Detail: err.Error()}
	}

	// Les erreurs hors fichier — configuration illisible, extension manquante —
	// sont des échecs de l'outil, pas du code. Les compter comme des violations
	// ferait chercher un problème dans le code qui n'y est pas.
	for _, message := range source.Errors {
		report.Cases = append(report.Cases, Case{
			Suite: "phpstan", Name: "erreur d'analyse",
			Status: StatusFailed, Message: firstLine(message),
		})
	}

	files := map[string]phpstanFile{}
	if len(source.Files) > 0 {
		// Ignoré délibérément : `files: []` est un tableau vide, pas un objet, et
		// signifie « aucune violation ».
		_ = json.Unmarshal(source.Files, &files)
	}

	for _, path := range sortedKeys(files) {
		for _, message := range files[path].Messages {
			rule := message.Identifier
			if rule == "" {
				// Les versions anciennes n'émettent pas d'identifiant : le message
				// tient lieu de règle. Moins stable, mais mieux qu'une identité
				// fondée sur la ligne.
				rule = firstLine(message.Message)
			}
			report.Cases = append(report.Cases, Case{
				Suite: path, Name: rule, Status: StatusFailed,
				Message: fmt.Sprintf("ligne %d: %s", message.Line, firstLine(message.Message)),
			})
		}
	}

	report.tally()
	// Une analyse statique propre n'a aucun cas. Le verdict dérivé serait `na`,
	// c'est-à-dire « non exécutée » — or elle a bien tourné et n'a rien trouvé,
	// ce qui est le meilleur résultat possible.
	if report.Total == 0 {
		report.Status = ReportPass
	}
	report.Summary = summarizeViolations(report, "violation(s)")
	return report, nil
}

type csFixerReport struct {
	Files []struct {
		Name        string   `json:"name"`
		AppliedFixe []string `json:"appliedFixers"`
		Diff        string   `json:"diff"`
	} `json:"files"`
}

// ParseCSFixer lit la sortie JSON de PHP-CS-Fixer.
//
// L'outil tourne en `--dry-run` dans le pipeline : chaque fichier listé est un
// fichier qui **aurait** été modifié, donc un écart de style.
func ParseCSFixer(raw []byte) (Report, error) {
	report := Report{Family: FamilyStatic, Tool: "cs-fixer"}

	var source csFixerReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return report, ErrUnsupported{Tool: "cs-fixer", Detail: err.Error()}
	}

	for _, file := range source.Files {
		fixers := file.AppliedFixe
		if len(fixers) == 0 {
			fixers = []string{"style"}
		}
		for _, fixer := range fixers {
			// Une entrée par règle et non par fichier : c'est la granularité à
			// laquelle on corrige, et celle à laquelle « c'est réglé » a un sens.
			report.Cases = append(report.Cases, Case{
				Suite: file.Name, Name: fixer, Status: StatusFailed,
				Message: "écart de style : " + fixer,
				Trace:   file.Diff,
			})
		}
	}

	report.tally()
	if report.Total == 0 {
		report.Status = ReportPass
	}
	report.Summary = summarizeViolations(report, "écart(s) de style")
	return report, nil
}

func summarizeViolations(report Report, noun string) string {
	if report.Total == 0 {
		return "aucun " + strings.TrimSuffix(noun, "(s)")
	}
	files := map[string]bool{}
	for _, testCase := range report.Cases {
		files[testCase.Suite] = true
	}
	return fmt.Sprintf("%d %s dans %d fichier(s)", report.Total, noun, len(files))
}

// sortedKeys rend les clés triées.
//
// L'ordre d'itération d'une map est aléatoire en Go : sans tri, deux lectures du
// même rapport produiraient deux ordres de cas différents, et toute comparaison
// de sorties deviendrait illisible.
func sortedKeys[V any](source map[string]V) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
