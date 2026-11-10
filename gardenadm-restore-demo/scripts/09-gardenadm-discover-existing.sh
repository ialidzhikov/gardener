#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

docker cp $KUBECONFIG_GARDEN $CONTROL_PLANE_NODE:/virtual-garden-kubeconfig
docker exec $CONTROL_PLANE_NODE mkdir -p $DISCOVER_OUTPUT_ON_NODE

echo "> Running 'gardenadm discover existing'..."
docker exec $CONTROL_PLANE_NODE gardenadm discover existing --name root --namespace garden \
  --kubeconfig /virtual-garden-kubeconfig -d $DISCOVER_OUTPUT_ON_NODE

docker exec $CONTROL_PLANE_NODE rm -f "$DISCOVER_OUTPUT_ON_NODE/lease-self-hosted-shoot-root.yaml"
docker exec $CONTROL_PLANE_NODE ls $DISCOVER_OUTPUT_ON_NODE

echo "✅ Success!"
