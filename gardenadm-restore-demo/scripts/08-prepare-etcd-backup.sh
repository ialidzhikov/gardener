#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

echo "> Copying the etcd backup from the backup storage (host) onto the new '$CONTROL_PLANE_NODE' machine container..."
docker cp "$REPO_ROOT/dev/local-backupbuckets" "$CONTROL_PLANE_NODE:$BACKUP_BUCKETS_ON_NODE"

backup_data_path="$(backup_data_path_on_node)"
echo "> etcd backup data path on the node: $backup_data_path"

echo "✅ Success!"
