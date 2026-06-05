#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

VIRTUAL_GARDEN_KUBECONFIG="${VIRTUAL_GARDEN_KUBECONFIG:-./dev-setup/kubeconfigs/virtual-garden/kubeconfig}"
SELFHOSTED_SHOOT_KUBECONFIG="${SELFHOSTED_SHOOT_KUBECONFIG:-./dev-setup/kubeconfigs/self-hosted-shoot/kubeconfig}"
SHOOT_NAMESPACE="${SHOOT_NAMESPACE:-garden}"
SHOOT_NAME="${SHOOT_NAME:-root}"

echo "> Verifying the default/experimental-configmap still exists after recovery..."
if ! ACTUAL_CM_CONTENT=$(kubectl --kubeconfig "$SELFHOSTED_SHOOT_KUBECONFIG" -n default get configmap experimental-configmap -o jsonpath='{.data.content}' 2>/dev/null); then
  echo "ERROR: default/experimental-configmap is missing after recovery — etcd data was not preserved" >&2
  exit 1
fi
EXPECTED_CONTENT='experimenting with control plane disaster recovery'
if [ "$ACTUAL_CM_CONTENT" != "$EXPECTED_CONTENT" ]; then
  echo "ERROR: default/experimental-configmap .data.content mismatch after recovery: got='$ACTUAL_CM_CONTENT' want='$EXPECTED_CONTENT'" >&2
  exit 1
fi
echo "> default/experimental-configmap survived recovery with expected content"

echo "> Verifying the Shoot UID was preserved across recovery..."
GARDEN_UID=$(kubectl --kubeconfig "$VIRTUAL_GARDEN_KUBECONFIG" -n "$SHOOT_NAMESPACE" get shoot "$SHOOT_NAME" -o jsonpath='{.status.uid}')
[ -n "$GARDEN_UID" ] || { echo "ERROR: Shoot .status.uid is empty in the garden cluster" >&2; exit 1; }

echo "> Reading statusUID from the kube-system/shoot-info ConfigMap on the self-hosted Shoot..."
SHOOT_INFO_UID=$(kubectl --kubeconfig "$SELFHOSTED_SHOOT_KUBECONFIG" -n kube-system get configmap shoot-info -o jsonpath='{.data.statusUID}')
[ -n "$SHOOT_INFO_UID" ] || { echo "ERROR: shoot-info ConfigMap has empty statusUID" >&2; exit 1; }

if [ "$GARDEN_UID" != "$SHOOT_INFO_UID" ]; then
  echo "ERROR: Shoot UID mismatch after recovery: garden=$GARDEN_UID shoot-info=$SHOOT_INFO_UID" >&2
  exit 1
fi
echo "> Shoot UID preserved across recovery: $GARDEN_UID"

echo "🎉 Success! The control plane Node was successfully restored!"
