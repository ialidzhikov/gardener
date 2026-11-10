#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Building gardenadm binary..."
make -C "$REPO_ROOT" gardenadm

echo "> Connecting self-hosted Shoot to the garden cluster..."
connect_command="$(KUBECONFIG=$KUBECONFIG_GARDEN "$REPO_ROOT/bin/gardenadm" token create --print-connect-command --shoot-namespace garden --shoot-name root)"
docker exec gind-machine-0 bash -c "$connect_command"

echo "> Waiting until the Shoot is reconciled..."
KUBECONFIG=$KUBECONFIG_GARDEN NAMESPACE=garden TIMEOUT=600 "$REPO_ROOT/hack/usage/wait-for.sh" shoot root \
  GardenletReady APIServerAvailable ControlPlaneHealthy ObservabilityComponentsHealthy \
  EveryNodeReady SystemComponentsHealthy BackupBucketsReady

echo "> Waiting until the ShootState is created..."
until kubectl --kubeconfig $KUBECONFIG_GARDEN -n garden get shootstate root &>/dev/null; do
  echo "⏳ Waiting for ShootState garden/root..."; sleep 5
done

echo "✅ Success!"
