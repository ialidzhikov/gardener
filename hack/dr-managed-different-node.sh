#!/usr/bin/env bash

set -e

PF_PID=""

function targetKind() {
    export KUBECONFIG="${KIND_KUBECONFIG:-"$PWD/example/gardener-local/kind/multi-zone/kubeconfig"}"
}

function targetMachine() {
    local ns="${1:-}"
    local machine="${2:-}"

    if [ "$ns" = "stop" ] || [ "$machine" = "stop" ]; then
        if [ -n "${PF_PID:-}" ]; then
            kill "$PF_PID" 2>/dev/null || true
            PF_PID=""
        fi
        return 0
    fi

    if [ -z "$ns" ] || [ -z "$machine" ]; then
        echo "targetMachine: namespace and machine are required" >&2
        return 1
    fi

    if [ -n "${PF_PID:-}" ]; then
        kill "$PF_PID" 2>/dev/null || true
        PF_PID=""
    fi

    kubectl -n "$ns" port-forward "pod/${machine}" 6443:443 >/dev/null 2>&1 &
    PF_PID=$!
    trap 'targetMachine stop' EXIT
    sleep 1

    kubectl -n "$ns" exec -it "$machine" -- cat /etc/kubernetes/admin.conf | sed 's/api.root.garden.local.gardener.cloud/localhost:6443/' > /tmp/shoot--garden--root.conf
    export KUBECONFIG=/tmp/shoot--garden--root.conf
}

function copy_data() {
    # Retry copy until tar doesn't warn "file changed as we read it"
    ns=$1
    pod=$2
    max_attempts=100
    success=0
    for i in $(seq 1 "$max_attempts"); do
        rm -rf data
        output=$(kubectl cp "$ns/$pod":/var/lib/etcd-main/data data 2>&1 || true)
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

# Managed infra setup
rm -rf secrets.yaml
rm -rf data
make kind-single-node-up
targetKind
make gardenadm-up SCENARIO=managed-infra
make gardenadm-up
kubectl label node gardener-operator-local-control-plane "worker.gardener.cloud/pool=control-plane"
export IMAGEVECTOR_OVERWRITE=$PWD/dev-setup/gardenadm/resources/generated/.imagevector-overwrite.yaml
go run ./cmd/gardenadm bootstrap -d ./dev-setup/gardenadm/resources/generated/managed-infra

# Creating workload
machine="$(kubectl -n shoot--garden--root get po -l app=machine -oname | head -1 | cut -d/ -f2)"
targetMachine shoot--garden--root "$machine"
./hack/creating-workload.sh

# Get etcd data
targetMachine stop
targetKind
rm -rf data
copy_data shoot--garden--root "$machine"

# Get worker machines CR and prepare etcd data
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- mkdir -p /var/lib/etcd-main
kubectl cp data/ gardenadm-unmanaged-infra/machine-0:/var/lib/etcd-main/data
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --bootstrap
targetMachine gardenadm-unmanaged-infra machine-0
rm -rf secrets.yaml
./hack/prep-cluster-2.sh "$machine"
rm -rf machine.yaml
kubectl get machine -l name=shoot--garden--root-worker-z1 -o yaml > machine.yaml
sed -i 's/namespace: kube-system/namespace: shoot--garden--root/' machine.yaml

# Put worker machines in kind so that kind mcm does not delete them
targetMachine stop
targetKind
kubectl apply -f machine.yaml

# Get updated etcd data
rm -rf data
copy_data gardenadm-unmanaged-infra machine-0

# Nuke control plane machine and create empty machine
kubectl -n shoot--garden--root scale deploy/machine-controller-manager --replicas=1
kubectl delete machine -l name=shoot--garden--root-control-plane-z1 -A
sleep 20
kubectl -n shoot--garden--root scale deploy/machine-controller-manager --replicas=0

# Get newly created machine by kind mcm
kubectl get machine -l name=shoot--garden--root-control-plane-z1 -A -o yaml > control-plane-machines.yaml
sed -i 's/namespace: shoot--garden--root/namespace: kube-system/' control-plane-machines.yaml

# Nuke machine for second preparation
kubectl -n gardenadm-unmanaged-infra delete pod machine-0 --force
sleep 3

# Put kind mcm created machine in etcd data so that cluster mcm does not delete it
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- mkdir -p /var/lib/etcd-main
kubectl cp data/ gardenadm-unmanaged-infra/machine-0:/var/lib/etcd-main/data
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --bootstrap

targetMachine gardenadm-unmanaged-infra machine-0
kubectl apply -f control-plane-machines.yaml

# Get updated etcd data
targetMachine stop
targetKind
rm -rf data
copy_data gardenadm-unmanaged-infra machine-0

# Set coredns configmap with new control plane IP
kubectl -n gardener-extension-provider-local-coredns get configmap coredns-custom -o yaml > dns.yaml
IP=$(kubectl get pods -A -o wide | grep machine-shoot--garden--root-control-plane  | grep -o -E "10.0.212.\S+")
sed -i "s/10.0.212../$IP/" dns.yaml
kubectl apply -f dns.yaml

# Recover
go run ./cmd/gardenadm bootstrap -d ./dev-setup/gardenadm/resources/generated/managed-infra --recover

# Delete artefacts
rm -rf secrets.yaml
rm -rf data
rm -rf machine.yaml
rm -rf control-plane-machines.yaml
rm -rf dns.yaml
rm -rf pod.yaml