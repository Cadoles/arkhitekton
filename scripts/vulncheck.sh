#!/usr/bin/env sh
#
# govulncheck, with explicit and noisy exceptions.
#
# govulncheck offers no exclusion mechanism: it reports, and exits with 3. With no filter, a
# vulnerability with no upstream fix makes the job red forever — and a blocking job that fails
# permanently stops being read. The day an eighth vulnerability appears, nobody sees it.
#
# The filter therefore exists to preserve the signal, never to silence it. Three properties
# make it defensible:
#
#   - **Every exception is named and justified** in the table below. A line with no
#     justification has no place here.
#   - **The exceptions are displayed again at every run.** They do not disappear from the log;
#     they only stop causing a failure.
#   - **An exception that has become useless makes the script fail.** An exclusion kept after
#     the upstream fix is a lie that lasts, and this is the only way of noticing it.
#
# The format parsed is the text output: the `Vulnerability #N: GO-...` line has been stable for
# years, and parsing it in shell avoids imposing a JSON parser on the runner.
#
# The tool is installed then invoked, and not launched through `go run`: the latter returns 1
# whatever the program's code, which makes "vulnerabilities were found" (3) and "the tool could
# not conclude" indistinguishable.
#
# Its version is pinned. The advisory database, for its part, is fetched at run time: pinning
# the tool therefore does not reduce the coverage, and avoids a silent upgrade of govulncheck
# changing the verdict with no commit explaining it.

set -eu

GOVULNCHECK_VERSION=v1.7.0

# ─── Exceptions ──────────────────────────────────────────────────────────────
#
# GO-2023-1901 — Pipelines do not validate child UIDs (CVE-2023-37264)
#
#   Set aside: the vulnerable code is absent from the binary.
#
#   The advisory declares no vulnerable symbol. Deprived of that information, govulncheck marks
#   the whole module as reachable as soon as it is imported — hence traces that are only `init`
#   and `DeepCopy` calls, never a real call.
#
#   The advisory does name the code at fault, though:
#   `pkg/reconciler/pipelinerun/pipelinerun.go` lines 1358-1372, in Tekton's reconciler. The
#   dashboard is a **client**: it reads and creates PipelineRuns through the API, it runs no
#   reconciliation loop.
#
#   The check, reproducible:
#
#       go list -deps ./cmd/... ./internal/... | grep reconciler
#
#   returns `pkg/reconciler/pipeline/dag` — a graph utility pulled in by the API types — and
#   nothing else. `pkg/reconciler/pipelinerun` is absent.
#
#   No upstream fix exists (`Fixed in: N/A`). To be re-examined if Tekton publishes one, or if
#   the dashboard ever starts reconciling anything.
#
#   Re-examine: 2027-02-14
ACCEPTED="GO-2023-1901"

# ─── Run ─────────────────────────────────────────────────────────────────────

BIN_DIR=$(go env GOPATH)/bin
if [ ! -x "$BIN_DIR/govulncheck" ] || \
   ! "$BIN_DIR/govulncheck" -version 2>/dev/null | grep -q "${GOVULNCHECK_VERSION#v}"; then
    echo "installation de govulncheck $GOVULNCHECK_VERSION..."
    GOFLAGS=-mod=mod go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION"
fi

output=$("$BIN_DIR/govulncheck" "$@" 2>&1) && code=0 || code=$?

# An exit other than 0 (nothing found) or 3 (vulnerabilities found) reports a failure of the
# tool itself — network, advisory database unreachable. Telling it apart avoids taking an
# outage for a clean bill of health.
if [ "$code" -ne 0 ] && [ "$code" -ne 3 ]; then
    printf '%s\n' "$output"
    echo "govulncheck n'a pas pu conclure (code $code)" >&2
    exit "$code"
fi

# The identifiers that actually affect the code called. The "Symbol Results" section precedes
# them; what follows concerns only modules that are required but not called, and has never
# failed this job.
found=$(printf '%s\n' "$output" \
    | sed -n '/=== Symbol Results ===/,/^Your code is affected\|^No vulnerabilities found/p' \
    | grep -oE 'GO-[0-9]{4}-[0-9]+' \
    | sort -u)

blocking=""
for id in $found; do
    case " $ACCEPTED " in
        *" $id "*)
            # Displayed again, not silenced.
            echo "vulnérabilité écartée explicitement : $id (voir scripts/vulncheck.sh)"
            ;;
        *)
            blocking="$blocking $id"
            ;;
    esac
done

# An exception that no longer matches anything was fixed upstream, or the code changed. Keeping
# it would suggest an analysis that is still valid.
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
