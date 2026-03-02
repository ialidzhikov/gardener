#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

function copy_etcd_data() {
    # Retry copy until tar doesn't warn "file changed as we read it"
    max_attempts=50
    success=0
    for i in $(seq 1 "$max_attempts"); do
        rm -rf data
        output=$(kubectl cp gardenadm-unmanaged-infra/machine-0:/var/lib/etcd-main/data data 2>&1 || true)
        echo "$output"
        if echo "$output" | grep -qi 'file changed as we read it'; then
            echo "Attempt $i/$max_attempts: tar reported 'file changed as we read it', retrying..."
            sleep 2
            continue
        fi
        success=1
        break
    done
    if [ "$success" -ne 1 ]; then
        echo "Failed to copy data after $max_attempts attempts due to 'file changed as we read it' error."
        exit 1
    fi
}

# Create the setup according to the local setup with gardenadm
make kind-single-node-up
export KUBECONFIG=$PWD/example/gardener-local/kind/multi-zone/kubeconfig
make gardenadm-up

# Create control plane Node
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources

# Join worker Nodes
JOIN_COMMAND_1=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm token create --print-join-command | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-1 -- $(echo $JOIN_COMMAND_1)

JOIN_COMMAND_2=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm token create --print-join-command | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-2 -- $(echo $JOIN_COMMAND_2)

# Fetch the cluster KUBECONFIG
kubectl -n gardenadm-unmanaged-infra port-forward pod/machine-0 6443:443 >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
sleep 1

kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- cat /etc/kubernetes/admin.conf | sed 's/api.root.garden.local.gardener.cloud/localhost:6443/' > /tmp/shoot--garden--root.conf
export KUBECONFIG=/tmp/shoot--garden--root.conf

# Create a dummy ConfigMap
kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: testy
  namespace: default
data:
  foo: bar
EOF

# Get the etcd data
export KUBECONFIG=$PWD/example/gardener-local/kind/multi-zone/kubeconfig
rm -rf data
copy_etcd_data

# Nuke the control plane machine
kill "$PF_PID" 2>/dev/null || true
kubectl -n gardenadm-unmanaged-infra delete pod machine-0 --force
sleep 3

# Recover
kubectl -n gardenadm-unmanaged-infra patch svc machine-0 --type='json' -p='[{"op":"replace","path":"/spec/selector/apps.kubernetes.io~1pod-index","value":"3"}]'

kubectl -n gardenadm-unmanaged-infra exec -it machine-3 -- mkdir -p /var/lib/etcd-main

kubectl cp data/ gardenadm-unmanaged-infra/machine-3:/var/lib/etcd-main/data
