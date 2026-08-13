#!/usr/bin/env bash
#
# Installe sur kind les CRD dont le dashboard a besoin pour tourner sans branche
# conditionnelle propre au développement — et rien d'autre : aucun contrôleur.
#
# Pourquoi de vraies CRD plutôt qu'un bouchon. Le dashboard valide au démarrage
# que chaque `promotion_suffix` correspond au `filterTags` d'une `ImagePolicy`, et
# que `accepts_prerelease` correspond à sa plage SemVer. C'est le garde-fou contre
# la promotion silencieusement sans effet (B7). Court-circuiter cette vérification
# en développement désactiverait le contrôle exactement là où il s'exerce le plus
# souvent. Avec les CRD réelles, le code de validation, les informers et le
# câblage des watches tournent tels quels.
#
# Ce que cela ne donne pas : le comportement de sélection. Un `filterTags` mal
# écrit ou une plage mal formée passera l'admission sans que rien ne le retienne,
# faute de contrôleur pour l'appliquer. Voir doc 11 §11.3.

set -euo pipefail

# Versions figées et empreintes vérifiées : une CRD qui change sous les pieds
# produit des échecs de validation qu'aucune bissection ne retrouve.
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
# Volontairement pas de copie dans ce dépôt. Le CRD appartient à `mse`
# (misc/k8s/kustomization/base/crds/portaldata-crd.yaml) et une copie ici
# divergerait en silence — le dashboard lirait alors un schéma que l'application
# n'écrit plus.
if kubectl get crd portaldata.mse.cnous.fr >/dev/null 2>&1; then
  echo "  déjà installée"
else
  echo "  ABSENTE. Elle est fournie par le dépôt mse :" >&2
  echo "    kubectl apply -f ../mse/misc/k8s/kustomization/base/crds/portaldata-crd.yaml" >&2
  exit 1
fi

echo "== Objets Flux de développement =="
kubectl apply -k "$(dirname "$0")/fixtures"
