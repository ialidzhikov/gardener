#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

# In Gardener v1.155.0, gardenadm restore requires the 'worker' pool to not run system components until
# https://github.com/gardener/gardener/issues/15951 is fixed. Temporarily disable it here; the EXIT
# trap reverts the file. Mutating this tracked file in place is safe because each Prow job runs in its
# own isolated clonerefs checkout.
#
# TODO(ialidzhikov): Remove once https://github.com/gardener/gardener/issues/15951 is resolved.
yq -i '(.spec.provider.workers[] | select(.name == "worker").systemComponents.allow) = false' \
  "$REPO_ROOT/dev-setup/gardenadm/resources/base/shoot.yaml"

echo "✅ Success!"
