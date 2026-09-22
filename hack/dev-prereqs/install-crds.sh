#!/usr/bin/env bash
#
# Installs on kind the CRDs the dashboard needs in order to run with no development-specific
# conditional branch — and nothing else: no controller.
#
# Why real CRDs rather than a stub. The dashboard checks at start-up that each
# `promotion_suffix` matches an `ImagePolicy`'s `filterTags`, and that `accepts_prerelease`
# matches its SemVer range. It is the guard rail against a promotion that is silently without
# effect. Short-circuiting that check in development would disable the control exactly where it
# applies most often. With the real CRDs, the validation code, the informers and the wiring of
# the watches run as they are.
#
# What this does not give: the selection behaviour. A badly written `filterTags` or a malformed
# range will pass admission with nothing holding it back, for want of a controller to apply it.

set -euo pipefail

# Frozen versions and checked digests: a CRD that changes under one's feet produces validation
# failures no bisection finds.
FLUX_CRDS=(
  "image-reflector-controller v1.2.4 c28662b43ee30cfe17b1b177577564f3b52ee112ef345b52dcdda8b77049bde7"
  "image-automation-controller v1.2.4 7d04980647085d68069549b40c4027cc5b0a5b6d463e397501d3f8b7b1c9b0c9"
  "kustomize-controller v1.9.4 b9e74622f9f3d87950f59641651fe0ea0220367c1d6f34a72c9591552afda292"
  "source-controller v1.9.4 5acf3b01c24f15647a162f2d43b1287f0d0156fab0026f4afb9865874cdbe16d"
)

workdir=$(mktemp -d)
trap 'rm -rf "${workdir}"' EXIT

echo "== CRD Flux =="
for entry in "${FLUX_CRDS[@]}"; do
  read -r name version sha <<<"${entry}"
  file="${workdir}/${name}.crds.yaml"

  curl -sSLo "${file}" \
    "https://github.com/fluxcd/${name}/releases/download/${version}/${name}.crds.yaml"
  echo "${sha}  ${file}" | sha256sum -c - >/dev/null

  kubectl apply -f "${file}" >/dev/null
  echo "  ${name} ${version}"
done

echo "== CRD PortalData =="
# Deliberately no copy in this repository. The CRD belongs to `mse`
# (misc/k8s/kustomization/base/crds/portaldata-crd.yaml) and a copy here would diverge silently
# — the dashboard would then read a schema the application no longer writes.
if kubectl get crd portaldata.mse.cnous.fr >/dev/null 2>&1; then
  echo "  already installed"
else
  echo "  MISSING. It is supplied by the mse repository:" >&2
  echo "    kubectl apply -f ../mse/misc/k8s/kustomization/base/crds/portaldata-crd.yaml" >&2
  exit 1
fi

echo "== Development Flux objects =="
kubectl apply -k "$(dirname "$0")/fixtures"
