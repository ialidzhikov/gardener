#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

# Provisions the kind cluster and the Gardener control plane running on it.
# The DR scripts (hack/dr-unmanaged-*.sh) reuse this setup instead of recreating it on every run, so
# run this script once beforehand.

echo "> Setting up kind cluster..."
make kind-up

echo "> Setting up Gardener control plane in the kind cluster..."
make gardenadm-up SCENARIO=connect-kind
