#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "Creating a 'precious-data' ConfigMap..."
kubectl --kubeconfig $KUBECONFIG_SHOOT -n default create configmap precious-data --from-literal=content="please survive the disaster"

echo "✅ Success!"
