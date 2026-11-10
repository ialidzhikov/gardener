#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

# Mirrors the assertions of test/e2e/gardenadm/unmanagedinfra/restore.go.

fail() {
  echo "❌ $*" >&2
  exit 1
}

# A Node is healthy (see pkg/utils/kubernetes/health.CheckNode) if its Ready condition is True and none of the
# pressure/network conditions, if present, is anything other than False. Prints the names of unhealthy Nodes.
unhealthy_nodes() {
  kubectl --kubeconfig "$KUBECONFIG_SHOOT" get nodes -o json | yq -p json '
    .items[]
    | select(
        ((.status.conditions // []) | map(select(.type == "Ready" and .status == "True")) | length) == 0
        or
        ((.status.conditions // []) | map(select(
          (.type == "DiskPressure" or .type == "MemoryPressure" or .type == "NetworkUnavailable" or .type == "PIDPressure")
          and .status != "False")) | length) > 0
      )
    | .metadata.name'
}

echo "> Waiting until all Nodes are healthy (timeout 5m)..."
timeout=300
until node_count="$(kubectl --kubeconfig "$KUBECONFIG_SHOOT" get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')" \
  && [[ "$node_count" -gt 0 ]] \
  && unhealthy="$(unhealthy_nodes 2>/dev/null)" \
  && [[ -z "$unhealthy" ]]; do
  (( timeout > 0 )) || fail "Nodes are not healthy after 5m: ${unhealthy:-<no nodes found>}"
  echo "⏳ Waiting for Nodes to become healthy: ${unhealthy:-<no nodes found>}"
  sleep 5
  timeout=$((timeout - 5))
done
kubectl --kubeconfig "$KUBECONFIG_SHOOT" get nodes

echo "> Verifying the default/precious-data ConfigMap survived recovery..."
content="$(kubectl --kubeconfig "$KUBECONFIG_SHOOT" -n default get configmap precious-data -o jsonpath='{.data.content}')" \
  || fail "default/precious-data should have survived recovery"
[[ "$content" == "please survive the disaster" ]] \
  || fail "default/precious-data should have survived recovery with its original content, got: '$content'"
echo "   content: $content"

echo "> Verifying the Shoot UID was preserved across recovery..."
garden_uid="$(kubectl --kubeconfig "$KUBECONFIG_GARDEN" -n garden get shoot root -o jsonpath='{.status.uid}')"
[[ -n "$garden_uid" ]] || fail "Shoot .status.uid should not be empty in the garden cluster"
shoot_info_uid="$(kubectl --kubeconfig "$KUBECONFIG_SHOOT" -n kube-system get configmap shoot-info -o jsonpath='{.data.statusUID}')"
[[ "$shoot_info_uid" == "$garden_uid" ]] \
  || fail "the shoot-info statusUID ('$shoot_info_uid') should match the garden Shoot .status.uid ('$garden_uid') after recovery"
echo "   garden Shoot .status.uid:  $garden_uid"
echo "   shoot-info statusUID:      $shoot_info_uid"

echo "> Verifying gardenlet was scaled back to 2 replicas..."
available_replicas="$(kubectl --kubeconfig "$KUBECONFIG_SHOOT" -n kube-system get deployment gardenlet -o jsonpath='{.status.availableReplicas}')"
[[ "$available_replicas" == "2" ]] \
  || fail "gardenlet Deployment should have 2 available replicas after restore, got: '${available_replicas:-0}'"
echo "   available replicas: $available_replicas"

echo "✅ The control plane Node was successfully restored (Shoot UID: $garden_uid)"
