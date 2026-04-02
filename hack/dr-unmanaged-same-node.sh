#!/usr/bin/env bash

set -e

# Remove old data and secrets
rm -rf secrets.yaml
rm -rf data
rm -rf pod.yaml

PF_PID=""

function targetKind() {
    export KUBECONFIG="${KIND_KUBECONFIG:-"$PWD/dev-setup/kubeconfigs/runtime/kubeconfig"}"
}

function targetMachine() {
    local machine="${1:-}"
    if [ -z "$machine" ]; then
        echo "targetMachine: machine is required" >&2
        return 1
    fi

    if [ "$machine" = "stop" ]; then
        if [ -n "${PF_PID:-}" ]; then
            kill "$PF_PID" 2>/dev/null || true
            PF_PID=""
        fi
        return 0
    fi

    if [ -n "${PF_PID:-}" ]; then
        kill "$PF_PID" 2>/dev/null || true
        PF_PID=""
    fi

    kubectl -n gardenadm-unmanaged-infra port-forward "pod/${machine}" 6443:443 >/dev/null 2>&1 &
    PF_PID=$!
    trap 'targetMachine stop' EXIT
    sleep 1
    kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- cat /etc/kubernetes/admin.conf | sed 's/api.root.garden.external.local.gardener.cloud/localhost:6443/' > /tmp/shoot--garden--root.conf
    export KUBECONFIG=/tmp/shoot--garden--root.conf
}

function copy_data() {
    # Retry copy until tar doesn't warn "file changed as we read it"
    max_attempts=20
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

# Setup kind and gardenadm
make kind-single-node-up
targetKind
make gardenadm-up

# 1 control-plane node and 2 worker nodes setup
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources
JOIN_COMMAND_1=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm token create --print-join-command | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-1 -- $JOIN_COMMAND_1
JOIN_COMMAND_2=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm token create --print-join-command | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-2 -- $JOIN_COMMAND_2

# Creating dummy workload
targetMachine machine-0
./hack/creating-workload.sh

# Switch to kind
targetMachine stop
targetKind

# Run GAPI
kubectl label node gardener-operator-local-control-plane worker.gardener.cloud/pool=control-plane
make gardenadm-up SCENARIO=connect
hack/usage/generate-virtual-garden-admin-kubeconf.sh > /tmp/virtual-garden-kubeconfig
kubectl --kubeconfig /tmp/virtual-garden-kubeconfig get namespaces

# Deploy gardenlet and register shoot to GAPI
make gardenadm
JOIN_COMMAND_3=$(KUBECONFIG=/tmp/virtual-garden-kubeconfig ./bin/gardenadm token create --print-connect-command --shoot-namespace=garden --shoot-name=root | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- $JOIN_COMMAND_3
kubectl --kubeconfig /tmp/virtual-garden-kubeconfig -n garden patch shoot root --subresource status --type=merge --patch='{"status":{"lastOperation":{"state": "Succeeded"}}}'
targetMachine machine-0
kubectl delete pod -l role=gardenlet
targetMachine stop
targetKind
sleep 15

# Move shoot to machine-0 and discover it with gardenadm
kubectl cp /tmp/virtual-garden-kubeconfig gardenadm-unmanaged-infra/machine-0:/tmp/virtual-garden-kubeconfig
kubectl cp dev-setup/gardenadm/resources/base/shoot.yaml gardenadm-unmanaged-infra/machine-0:shoot.yaml
kubectl -n gardenadm-unmanaged-infra exec -it machine-0  -- gardenadm discover shoot.yaml --kubeconfig /tmp/virtual-garden-kubeconfig
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- sh -c 'find . -maxdepth 1 -type f | grep backup | xargs -I {} mv {} gardenadm/resources/'
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- sh -c 'find . -maxdepth 1 -type f | grep shootstate | xargs -I {} mv {} gardenadm/resources/'

# Wait for etcd snapshot to contain the workload data
echo "Waiting for 6 minutes before copying data to have a backup with more data in it..."
sleep 360

# Nuke machine
kubectl -n gardenadm-unmanaged-infra delete pod machine-0 --force
sleep 3

# First phase of recovery
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --bootstrap
targetMachine machine-0
./hack/prep-cluster-2.sh machine-0

# Switch to kind
targetMachine stop
targetKind

# Transfer data
copy_data

# Cleanup gardenadm os artefacts
kubectl -n gardenadm-unmanaged-infra delete pod machine-0 --force
sleep 3

# Move data to machine-0
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- mkdir -p /var/lib/etcd-main
kubectl cp data/ gardenadm-unmanaged-infra/machine-0:/var/lib/etcd-main/data

# Second phase of recovery
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 --  gardenadm init -d /gardenadm/resources  --use-bootstrap-etcd || true

# Remove old data and secrets
rm -rf secrets.yaml
rm -rf data
rm -rf pod.yaml
rm -f dr-unmanaged-backupbucket.yaml
rm -f dr-unmanaged-backupentry.yaml