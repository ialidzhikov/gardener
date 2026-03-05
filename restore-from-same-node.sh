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
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --use-bootstrap-etcd


# TODO(ialidzhikov): Add support for restoring a self-hosted Shoot with worker Nodes.
# 
# # Join worker Nodes
# JOIN_COMMAND_1=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm token create --print-join-command | tr -d '"')
# kubectl -n gardenadm-unmanaged-infra exec -it machine-1 -- $(echo $JOIN_COMMAND_1)

# JOIN_COMMAND_2=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm token create --print-join-command | tr -d '"')
# kubectl -n gardenadm-unmanaged-infra exec -it machine-2 -- $(echo $JOIN_COMMAND_2)

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

# Get the ShootState
kubectl -n gardenadm-unmanaged-infra cp machine-0:/tmp/shootstate.yaml /tmp/shootstate.yaml

# Nuke the control plane machine
kill "$PF_PID" 2>/dev/null || true
kubectl -n gardenadm-unmanaged-infra delete pod machine-0 --force
sleep 3

echo "Waiting until the gardenadm-unmanaged-infra/machine-0 Pod is created again..."
kubectl -n gardenadm-unmanaged-infra wait --for=condition=Ready pod/machine-0

# Copy the etcd data to the new Node
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- mkdir -p /var/lib/etcd-main
kubectl cp data/ gardenadm-unmanaged-infra/machine-0:/var/lib/etcd-main/data

# Copy the ShootState to the new Node
kubectl cp /tmp/shootstate.yaml gardenadm-unmanaged-infra/machine-0:/gardenadm/resources/shootstate.yaml

kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --use-bootstrap-etcd || true

# Hacks / Workarounds
# Delete the Node object
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- kubectl --kubeconfig=/etc/kubernetes/admin.conf delete node machine-0

# Delete all Pods on machine-0
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- kubectl --kubeconfig=/etc/kubernetes/admin.conf delete pods --all-namespaces --field-selector spec.nodeName=machine-0 --force --grace-period=0

# Delete the shoot-core-coredns ManagedResource
# This managed resource is used by gardenadm to check if the Pod network is set up or not.
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- kubectl --kubeconfig=/etc/kubernetes/admin.conf -n kube-system delete managedresource shoot-core-coredns --wait=false
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- kubectl --kubeconfig=/etc/kubernetes/admin.conf -n kube-system patch  managedresource shoot-core-coredns --patch '{"metadata":{"finalizers":[]}}' --type=merge

# Approve manually the CSR
GNA_CSR_NAME=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- kubectl --kubeconfig=/etc/kubernetes/admin.conf get csr --sort-by=metadata.creationTimestamp -o json | jq -r '.items[-1].metadata.name')
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- kubectl --kubeconfig=/etc/kubernetes/admin.conf certificate approve $GNA_CSR_NAME

# Retry the restore of the Node
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --use-bootstrap-etcd
