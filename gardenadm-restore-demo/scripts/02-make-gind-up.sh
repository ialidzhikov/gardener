#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Setting up self-hosted Shoot cluster..."
make -C "$REPO_ROOT" gind-up SCENARIO=join

echo "✅ Success!"
