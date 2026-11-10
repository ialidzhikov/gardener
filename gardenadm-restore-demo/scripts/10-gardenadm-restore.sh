#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

backup_data_path="$(backup_data_path_on_node)"

echo "> Running 'gardenadm restore'..."
docker exec "$CONTROL_PLANE_NODE" \
  gardenadm restore \
  --backup-data-path "$backup_data_path" \
  --config-dir "$DISCOVER_OUTPUT_ON_NODE" \
  --prior-node-name "$CONTROL_PLANE_NODE"

echo "✅ Success!"
