#!/usr/bin/env sh
#
# govulncheck, avec des exceptions explicites et bruyantes.
#
# govulncheck n'offre aucun mécanisme d'exclusion : il signale, et sort en 3.
# Sans filtre, une vulnérabilité sans correctif amont rend le job rouge pour
# toujours — et un job bloquant qui échoue en permanence cesse d'être lu. Le
# jour où une huitième vulnérabilité apparaît, personne ne la voit.
#
# Le filtre existe donc pour préserver le signal, jamais pour le taire. Trois
# propriétés le rendent défendable :
#
#   - **Chaque exception est nommée et justifiée** dans le tableau ci-dessous.
#     Une ligne sans justification n'a pas sa place ici.
#   - **Les exceptions sont réaffichées à chaque exécution.** Elles ne
#     disparaissent pas du journal ; elles cessent seulement de faire échouer.
#   - **Une exception devenue inutile fait échouer le script.** Une exclusion
#     qu'on garde après le correctif amont est un mensonge qui dure, et c'est
#     la seule façon de s'en apercevoir.
#
# Le format analysé est la sortie texte : la ligne `Vulnerability #N: GO-...`
# est stable depuis des années, et l'analyser en shell évite d'imposer un
# analyseur JSON au runner.
#
# L'outil est installé puis invoqué, et non lancé par `go run` : ce dernier
# rend 1 quel que soit le code du programme, ce qui rend indiscernables « des
# vulnérabilités ont été trouvées » (3) et « l'outil n'a pas pu conclure ».
#
# Sa version est épinglée. La base d'avis, elle, est récupérée à l'exécution :
# épingler l'outil ne réduit donc pas la couverture, et évite qu'une montée
# silencieuse de govulncheck change le verdict sans qu'aucun commit ne
# l'explique.

set -eu

GOVULNCHECK_VERSION=v1.7.0

# ─── Exceptions ──────────────────────────────────────────────────────────────
#
# GO-2023-1901 — Pipelines do not validate child UIDs (CVE-2023-37264)
#
#   Écartée : le code vulnérable est absent du binaire.
#
#   L'avis ne déclare aucun symbole vulnérable. Privé de cette information,
#   govulncheck marque le module entier comme atteignable dès qu'on l'importe —
#   d'où des traces qui ne sont que des `init` et des `DeepCopy`, jamais un
#   appel réel.
#
#   L'avis nomme pourtant le code en cause :
#   `pkg/reconciler/pipelinerun/pipelinerun.go` lignes 1358-1372, dans le
#   réconciliateur de Tekton. Le dashboard est un **client** : il lit et crée
#   des PipelineRun par l'API, il n'exécute aucune boucle de réconciliation.
#
#   Vérification, reproductible :
#
#       go list -deps ./cmd/... ./internal/... | grep reconciler
#
#   rend `pkg/reconciler/pipeline/dag` — un utilitaire de graphe tiré par les
#   types d'API — et rien d'autre. `pkg/reconciler/pipelinerun` est absent.
#
#   Aucun correctif amont n'existe (`Fixed in: N/A`). À réexaminer si Tekton en
#   publie un, ou si le dashboard se met un jour à réconcilier quoi que ce soit.
#
#   Réexamen : 2027-02-14
#
ACCEPTED="GO-2023-1901"

# ─── Exécution ───────────────────────────────────────────────────────────────

BIN_DIR=$(go env GOPATH)/bin
if [ ! -x "$BIN_DIR/govulncheck" ] || \
   ! "$BIN_DIR/govulncheck" -version 2>/dev/null | grep -q "${GOVULNCHECK_VERSION#v}"; then
    echo "installation de govulncheck $GOVULNCHECK_VERSION..."
    GOFLAGS=-mod=mod go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION"
fi

output=$("$BIN_DIR/govulncheck" "$@" 2>&1) && code=0 || code=$?

# Une sortie autre que 0 (rien trouvé) ou 3 (vulnérabilités trouvées) signale un
# échec de l'outil lui-même — réseau, base d'avis injoignable. Le distinguer
# évite de prendre une panne pour un satisfecit.
if [ "$code" -ne 0 ] && [ "$code" -ne 3 ]; then
    printf '%s\n' "$output"
    echo "govulncheck n'a pas pu conclure (code $code)" >&2
    exit "$code"
fi

# Les identifiants qui affectent réellement le code appelé. La section
# « Symbol Results » les précède ; ce qui suit ne concerne que des modules
# requis mais non appelés, et n'a jamais fait échouer ce job.
found=$(printf '%s\n' "$output" \
    | sed -n '/=== Symbol Results ===/,/^Your code is affected\|^No vulnerabilities found/p' \
    | grep -oE 'GO-[0-9]{4}-[0-9]+' \
    | sort -u)

blocking=""
for id in $found; do
    case " $ACCEPTED " in
        *" $id "*)
            # Réaffichée, pas tue.
            echo "vulnérabilité écartée explicitement : $id (voir scripts/vulncheck.sh)"
            ;;
        *)
            blocking="$blocking $id"
            ;;
    esac
done

# Une exception qui ne correspond plus à rien a été corrigée en amont, ou le
# code a changé. La garder ferait croire à une analyse encore valable.
for id in $ACCEPTED; do
    case " $found " in
        *" $id "*) ;;
        *)
            echo "l'exception $id ne correspond à aucune vulnérabilité détectée :" >&2
            echo "la retirer de scripts/vulncheck.sh — une exclusion périmée masque le jour où elle redeviendrait vraie" >&2
            exit 1
            ;;
    esac
done

if [ -n "$blocking" ]; then
    printf '%s\n' "$output"
    echo "" >&2
    echo "vulnérabilité(s) non écartée(s) :$blocking" >&2
    exit 3
fi

echo "aucune vulnérabilité bloquante"
