#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Recreating 'gind-machine-0' machine container..."
make -C "$REPO_ROOT" gind-up SCENARIO=machines

echo "✅ Success!"
