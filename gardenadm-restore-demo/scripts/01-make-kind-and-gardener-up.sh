#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Setting up kind cluster..."
make -C "$REPO_ROOT" kind-up

echo "> Setting up Gardener control plane in the kind cluster..."
make -C "$REPO_ROOT" gardenadm-up SCENARIO=connect-kind

echo "✅ Success!"
