#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Triggering etcd delta snapshot..."
docker exec gind-machine-0 curl -sk --fail https://localhost:8080/snapshot/delta

echo
echo "✅ Success!"
