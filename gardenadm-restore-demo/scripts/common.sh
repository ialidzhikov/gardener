#!/usr/bin/env bash
# Shared settings and helpers for the demo scripts. Source it, don't execute it.

REPO_ROOT="${REPO_ROOT:-$(realpath "$(dirname "${BASH_SOURCE[0]}")/../..")}"
KUBECONFIG_GARDEN="$REPO_ROOT/dev-setup/kubeconfigs/virtual-garden/kubeconfig"
KUBECONFIG_SHOOT="$REPO_ROOT/dev-setup/kubeconfigs/self-hosted-shoot/kubeconfig"

CONTROL_PLANE_NODE="gind-machine-0"
BACKUP_BUCKETS_ON_NODE="/local-backupbuckets"
DISCOVER_OUTPUT_ON_NODE="/gardenadm/discover-output"

# Prints the etcd-main backup directory on the control plane node (the value for 'gardenadm restore --backup-data-path').
# The node is the source of truth, so no state needs to be passed between scripts.
backup_data_path_on_node() {
  local path
  path="$(docker exec "$CONTROL_PLANE_NODE" find "$BACKUP_BUCKETS_ON_NODE" -type d -path '*/etcd-main/v2' ! -path '*garden*' | head -1)"
  if [[ -z "$path" ]]; then
    echo "❌ No etcd-main backup found under $CONTROL_PLANE_NODE:$BACKUP_BUCKETS_ON_NODE. Did you run 08-prepare-etcd-backup.sh?" >&2
    return 1
  fi
  echo "$path"
}
