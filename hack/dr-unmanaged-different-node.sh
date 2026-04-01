#!/usr/bin/env bash

set -e

# Remove old data and secrets
rm -rf secrets.yaml
rm -rf data
rm -rf pod.yaml

PF_PID=""

function targetKind() {
    export KUBECONFIG="${KIND_KUBECONFIG:-"$PWD/example/gardener-local/kind/multi-zone/kubeconfig"}"
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
    kubectl -n gardenadm-unmanaged-infra exec -it machine-3 -- cat /etc/kubernetes/admin.conf | sed 's/api.root.garden.local.gardener.cloud/localhost:6443/' > /tmp/shoot--garden--root.conf
    export KUBECONFIG=/tmp/shoot--garden--root.conf
}

function copy_data() {
    # Retry copy until tar doesn't warn "file changed as we read it"
    max_attempts=20
    success=0
    for i in $(seq 1 "$max_attempts"); do
        rm -rf data
        output=$(kubectl cp gardenadm-unmanaged-infra/machine-3:/var/lib/etcd-main/data data 2>&1 || true)
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

# Route KAPI traffic to machine-3 
kubectl -n gardenadm-unmanaged-infra patch svc machine-0 --type='json' -p='[{"op":"replace","path":"/spec/selector/apps.kubernetes.io~1pod-index","value":"3"}]'

# 1 control-plane node and 2 worker nodes setup
kubectl -n gardenadm-unmanaged-infra exec -it machine-3 -- gardenadm init -d /gardenadm/resources
JOIN_COMMAND_1=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-3 -- gardenadm token create --print-join-command | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-1 -- $JOIN_COMMAND_1
JOIN_COMMAND_2=$(kubectl -n gardenadm-unmanaged-infra exec -it machine-3 -- gardenadm token create --print-join-command | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-2 -- $JOIN_COMMAND_2

# Creating dummy workload
targetMachine machine-3
./hack/creating-workload.sh

# Wait for etcd snapshot to contain the workload data
echo "Waiting for 6 minutes before copying data to have a backup with more data in it..."
sleep 360

# Switch to kind
targetMachine stop
targetKind

# Nuke machine
kubectl -n gardenadm-unmanaged-infra delete pod machine-3 --force
sleep 3

# First phase of recovery
local_backupbucket=$(ls dev/local-backupbuckets)
kubectl -n gardenadm-unmanaged-infra exec -it machine-3 -- gardenadm init -d /gardenadm/resources --bootstrap --store-container ${local_backupbucket}
targetMachine machine-3
./hack/prep-cluster-2.sh machine-3

# Switch to kind
targetMachine stop
targetKind

# Transfer data
copy_data

# Cleanup gardenadm os artefacts
kubectl -n gardenadm-unmanaged-infra delete pod machine-3 --force

# Route KAPI traffic to machine-0
kubectl -n gardenadm-unmanaged-infra patch svc machine-0 --type='json' -p='[{"op":"replace","path":"/spec/selector/apps.kubernetes.io~1pod-index","value":"0"}]'

# Move data to machine-0
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- mkdir -p /var/lib/etcd-main
kubectl cp data/ gardenadm-unmanaged-infra/machine-0:/var/lib/etcd-main/data
kubectl cp secrets.yaml gardenadm-unmanaged-infra/machine-0:/secrets.yaml

# Second phase of recovery
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 --  gardenadm init -d /gardenadm/resources  --secret-file=/secrets.yaml --use-bootstrap-etcd || true

# Remove old data and secrets
rm -rf secrets.yaml
rm -rf data
rm -rf pod.yaml