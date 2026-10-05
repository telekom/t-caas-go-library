#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
#
# SPDX-License-Identifier: Apache-2.0
#
# kind-e2e.sh — runs the dynamiccache auth-operator e2e suite on a kind
# cluster. Missing kind/kubectl binaries and missing or mismatched Helm binaries
# are downloaded into ./.tools with versions pinned in versions.env. By default a throwaway cluster
# with a unique name is created and always deleted on exit; with
# E2E_USE_EXISTING_CLUSTER=true the cluster addressed by KUBECONFIG is used
# as-is (CI creates it with helm/kind-action). On failure the auth-operator
# namespace is dumped into ./artifacts for CI artifact collection.
#
# Never point E2E_USE_EXISTING_CLUSTER at a shared or production cluster: the
# suite installs auth-operator and creates cluster-scoped RBAC fixtures.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TOOLS_DIR="${SCRIPT_DIR}/.tools"
ARTIFACTS_DIR="${SCRIPT_DIR}/artifacts"

# shellcheck source=/dev/null
source "${REPO_ROOT}/versions.env"

: "${AUTH_OPERATOR_VERSION:?AUTH_OPERATOR_VERSION must be set in versions.env}"
: "${KIND_VERSION:?KIND_VERSION must be set in versions.env}"
: "${KUBECTL_VERSION:?KUBECTL_VERSION must be set in versions.env}"
: "${HELM_VERSION:?HELM_VERSION must be set in versions.env}"

AUTH_OPERATOR_NAMESPACE="${AUTH_OPERATOR_NAMESPACE:-auth-operator-system}"
USE_EXISTING_CLUSTER="${E2E_USE_EXISTING_CLUSTER:-false}"
CLUSTER_NAME="dynamiccache-e2e-${GITHUB_RUN_ID:-$$}"
if [ "${USE_EXISTING_CLUSTER}" != "true" ]; then
  # Isolated kubeconfig: never touches the caller's current context.
  KUBECONFIG="${TOOLS_DIR}/kubeconfig-${CLUSTER_NAME}"
  export KUBECONFIG
fi

mkdir -p "${TOOLS_DIR}" "${ARTIFACTS_DIR}"
export PATH="${TOOLS_DIR}:${PATH}"

OS="$(uname | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *)
    echo "FATAL: unsupported architecture $(uname -m)" >&2
    exit 1
    ;;
esac

log() { printf '\n=== %s\n' "$*"; }

fetch() {
  # fetch <url> <dest>
  log "downloading $1"
  curl --fail --silent --show-error --location --retry 3 --output "$2" "$1"
}

ensure_kind() {
  if command -v kind >/dev/null 2>&1; then return; fi
  fetch "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-${OS}-${ARCH}" "${TOOLS_DIR}/kind"
  chmod +x "${TOOLS_DIR}/kind"
}

ensure_kubectl() {
  if command -v kubectl >/dev/null 2>&1; then return; fi
  fetch "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${OS}/${ARCH}/kubectl" "${TOOLS_DIR}/kubectl"
  chmod +x "${TOOLS_DIR}/kubectl"
}

ensure_helm() {
  if command -v helm >/dev/null 2>&1 &&
    [ "$(helm version --template '{{.Version}}' 2>/dev/null)" = "${HELM_VERSION}" ]; then
    return
  fi
  fetch "https://get.helm.sh/helm-${HELM_VERSION}-${OS}-${ARCH}.tar.gz" "${TOOLS_DIR}/helm.tar.gz"
  tar -xzf "${TOOLS_DIR}/helm.tar.gz" -C "${TOOLS_DIR}" --strip-components 1 "${OS}-${ARCH}/helm"
  rm -f "${TOOLS_DIR}/helm.tar.gz"
  chmod +x "${TOOLS_DIR}/helm"
}

FAILED=1
cleanup() {
  if [ "${FAILED}" -ne 0 ]; then
    log "FAILURE — dumping ${AUTH_OPERATOR_NAMESPACE} namespace state to ${ARTIFACTS_DIR}"
    kubectl cluster-info dump \
      --namespaces "${AUTH_OPERATOR_NAMESPACE}" \
      --output-directory "${ARTIFACTS_DIR}/cluster-info-dump" \
      >/dev/null 2>"${ARTIFACTS_DIR}/dump-errors.log" || true
    kubectl get binddefinitions,roledefinitions -A -o yaml \
      >"${ARTIFACTS_DIR}/auth-operator-crs.yaml" 2>/dev/null || true
  fi
  if [ "${USE_EXISTING_CLUSTER}" != "true" ]; then
    log "deleting kind cluster ${CLUSTER_NAME}"
    kind delete cluster --name "${CLUSTER_NAME}" || true
    rm -f "${KUBECONFIG}"
  fi
}
trap cleanup EXIT

command -v go >/dev/null 2>&1 || { echo "FATAL: go is required" >&2; exit 1; }
ensure_kind
ensure_kubectl
ensure_helm

log "$(go version), kind $(kind version), $(kubectl version --client | head -n1), helm $(helm version --short)"

if [ "${USE_EXISTING_CLUSTER}" = "true" ]; then
  log "using existing cluster $(kubectl config current-context)"
else
  log "creating kind cluster ${CLUSTER_NAME}"
  kind create cluster --name "${CLUSTER_NAME}" --wait 5m
fi

log "installing auth-operator chart ${AUTH_OPERATOR_VERSION}"
helm install auth-operator oci://ghcr.io/telekom/charts/auth-operator \
  --version "${AUTH_OPERATOR_VERSION}" \
  --namespace "${AUTH_OPERATOR_NAMESPACE}" \
  --create-namespace \
  --wait --timeout 5m

log "waiting for auth-operator CRDs to be Established"
kubectl wait --for condition=Established --timeout 120s \
  crd/binddefinitions.authorization.t-caas.telekom.com \
  crd/roledefinitions.authorization.t-caas.telekom.com

log "waiting for auth-operator deployments to roll out"
# portable to bash 3.2 (macOS): no mapfile
deployments=()
while IFS= read -r deployment; do
  deployments+=("${deployment}")
done < <(kubectl --namespace "${AUTH_OPERATOR_NAMESPACE}" get deployments --output name)
if [ "${#deployments[@]}" -eq 0 ]; then
  log "FATAL: no deployments found in ${AUTH_OPERATOR_NAMESPACE} — chart layout changed?"
  exit 1
fi
for deployment in "${deployments[@]}"; do
  kubectl --namespace "${AUTH_OPERATOR_NAMESPACE}" rollout status "${deployment}" --timeout 300s
done

log "running e2e suite"
cd "${REPO_ROOT}"
E2E=true go test ./e2e/dynamiccache/... -v -count=1 -timeout 20m

FAILED=0
log "e2e suite passed"
