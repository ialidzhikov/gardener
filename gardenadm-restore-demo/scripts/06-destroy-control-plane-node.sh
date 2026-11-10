#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Stopping 'gind-machine-0' machine container..."
docker stop gind-machine-0

echo "> Deleting 'gind-machine-0' volume..."
docker rm --volumes gind-machine-0

echo "✅ Success!"
