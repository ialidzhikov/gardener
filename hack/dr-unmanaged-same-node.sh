#!/usr/bin/env bash

set -e

# Remove created yaml files
rm -rf pod.yaml

PF_PID=""

function targetKind() {
    export KUBECONFIG="${KIND_KUBECONFIG:-"$PWD/dev-setup/kubeconfigs/runtime/kubeconfig"}"
}

function targetMachine() {
    ./hack/usage/generate-admin-kubeconfig-local.sh self-hosted-shoot > "$PWD/dev-setup/kubeconfigs/self-hosted-shoot/kubeconfig"
    export KUBECONFIG="$PWD/dev-setup/kubeconfigs/self-hosted-shoot/kubeconfig"
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
targetKind

# Run GAPI
kubectl label node gardener-operator-local-control-plane worker.gardener.cloud/pool=control-plane
make gardenadm-up SCENARIO=connect

targetKind

kubectl --kubeconfig ./dev-setup/kubeconfigs/virtual-garden/kubeconfig get namespaces

# Deploy gardenlet and register shoot to GAPI
make gardenadm
JOIN_COMMAND_3=$(KUBECONFIG=./dev-setup/kubeconfigs/virtual-garden/kubeconfig ./bin/gardenadm token create --print-connect-command --shoot-namespace=garden --shoot-name=root | tr -d '"')
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- $JOIN_COMMAND_3
kubectl --kubeconfig ./dev-setup/kubeconfigs/virtual-garden/kubeconfig -n garden patch shoot root --subresource status --type=merge --patch='{"status":{"lastOperation":{"state": "Succeeded"}}}'
targetMachine machine-0
kubectl delete pod -l role=gardenlet
targetKind
sleep 15

# Move shoot to machine-0 and discover it with gardenadm
kubectl cp ./dev-setup/kubeconfigs/virtual-garden/kubeconfig gardenadm-unmanaged-infra/machine-0:/tmp/virtual-garden-kubeconfig
kubectl cp dev-setup/gardenadm/resources/base/shoot.yaml gardenadm-unmanaged-infra/machine-0:shoot.yaml
kubectl -n gardenadm-unmanaged-infra exec -it machine-0  -- gardenadm discover shoot.yaml --kubeconfig /tmp/virtual-garden-kubeconfig
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- sh -c 'find . -maxdepth 1 -type f | grep backup | xargs -I {} mv {} gardenadm/resources/'
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- sh -c 'find . -maxdepth 1 -type f | grep shootstate | xargs -I {} mv {} gardenadm/resources/'

# Nuke machine but retain IP address
machine_pod=$(docker exec -it gardener-operator-local-control-plane crictl pods | grep machine-0 | cut -d ' ' -f 1)
machine_container=$(docker exec -it gardener-operator-local-control-plane crictl ps | grep "$machine_pod" | cut -d ' ' -f 1)
docker exec -it gardener-operator-local-control-plane crictl stop "$machine_container"
sleep 100

# Recovery (bootstrap + prep + second phase)
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --recover --use-bootstrap-etcd

# Remove created yaml files
rm -rf pod.yaml