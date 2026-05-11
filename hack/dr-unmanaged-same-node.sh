#!/usr/bin/env bash

set -e

function targetKind() {
    export KUBECONFIG="$PWD/dev-setup/kubeconfigs/runtime/kubeconfig"
}

function targetMachine() {
    KUBECONFIG_SELFHOSTEDSHOOT_CLUSTER="$PWD/dev-setup/kubeconfigs/self-hosted-shoot/kubeconfig"
    ./hack/usage/generate-kubeconfig.sh self-hosted-shoot --docker gind-machine-0 > "$KUBECONFIG_SELFHOSTEDSHOOT_CLUSTER"
    export KUBECONFIG="$KUBECONFIG_SELFHOSTEDSHOOT_CLUSTER"
}

echo "> Setting up gind (machine containers only)..."
make gind-up SCENARIO=machines

echo "> Initializing control plane Node..."
# TODO: Can we use the "--use-bootstrap-etcd" flag?
docker exec -ti gind-machine-0 gardenadm init -d /gardenadm/resources

echo "> Joining gind-machine-1 worker Node..."
JOIN_COMMAND_1=$(docker exec -ti gind-machine-0 gardenadm token create --print-join-command | tr -d '"')
docker exec -ti gind-machine-1 $(echo $JOIN_COMMAND_1)
echo "> Joining gind-machine-2 worker Node..."
JOIN_COMMAND_2=$(docker exec -ti gind-machine-0 gardenadm token create --print-join-command | tr -d '"')
docker exec -ti gind-machine-2 $(echo $JOIN_COMMAND_2)

echo "> Creating dummy workload..."
targetMachine
./hack/create-workload.sh

echo "> Setting up Gardener control plane in the kind cluster..."
make kind-up
make gardenadm-up SCENARIO=connect-kind

echo "> Ensuring GRM is scheduled on the kind's control plane Node..."
targetKind
# This is related to a workaround in the PoC branch.
kubectl label node gardener-local-control-plane worker.gardener.cloud/pool=control-plane

echo "> Sanity checking that gardener-apiserver is running..."
kubectl --kubeconfig ./dev-setup/kubeconfigs/virtual-garden/kubeconfig get namespaces

echo "> Building gardenadm binary..."
make -B gardenadm

echo "> Connecting the Shoot cluster to Gardener..."
CONNECT_COMMAND=$(KUBECONFIG=./dev-setup/kubeconfigs/virtual-garden/kubeconfig ./bin/gardenadm token create --print-connect-command --shoot-namespace=garden --shoot-name=root | tr -d '"')
docker exec -ti gind-machine-0 $(echo $CONNECT_COMMAND)

echo "> Obtaining a ShootState resource for the Shoot..."
# Patching the Shoot status with a successful last operation is required to allow the shootstate-controller to create a ShootState for the Shoot
echo "> Patching the Shoot status with a successful create lastOperation..."
kubectl --kubeconfig ./dev-setup/kubeconfigs/virtual-garden/kubeconfig -n garden patch shoot root --subresource status --type=merge --patch='{"status":{"lastOperation":{"type": "Create","state": "Succeeded"}}}'

# Rolling out the gardenlet Deployment is required to trigger the shootstate-controller to create a ShootState for the Shoot
echo "> Rolling out the kube-system/gardenlet Deployment to trigger ShootState creation..."
targetMachine
kubectl -n kube-system rollout restart deployment/gardenlet
echo "> Waiting until the kube-system/gardenlet Deployment successfully rolled out..."
kubectl -n kube-system rollout status deployment/gardenlet
echo "> Waiting until the ShootState is created..."
for i in {1..6}; do
  if kubectl --kubeconfig ./dev-setup/kubeconfigs/virtual-garden/kubeconfig -n garden get shootstate root &> /dev/null; then
    break
  fi
  echo "> Attempt $i/6: Waiting until garden/root ShootState is created. Sleeping 10s..."
  sleep 10
done

sleep 15

echo "> Simulating a disaster event..."
echo "> Stopping the gind-machine-0 container..."
docker stop gind-machine-0
echo "> Deleting the gind-machine-0 container with its volumes..."
docker rm --volumes gind-machine-0

echo "> Setting up gind (recreating the gind-machine-0 container)..."
make gind-up SCENARIO=machines

echo "> Copying Shoot manifest and virtual garden kubeconfig to the gind-machine-0 container..."
docker cp ./dev-setup/kubeconfigs/virtual-garden/kubeconfig gind-machine-0:/virtual-garden-kubeconfig
docker cp ./dev-setup/gardenadm/resources/base/shoot.yaml gind-machine-0:/shoot.yaml

echo "> Downloading Gardener configuration resources for the Shoot..."
docker exec -ti gind-machine-0 gardenadm discover /shoot.yaml --kubeconfig /virtual-garden-kubeconfig
docker exec -ti gind-machine-0 sh -c 'find . -maxdepth 1 -type f | grep backup | xargs -I {} mv {} /gardenadm/resources/'
docker exec -ti gind-machine-0 sh -c 'find . -maxdepth 1 -type f | grep shootstate | xargs -I {} mv {} /gardenadm/resources/'

# TODO: Replace the hard-coded wait by triggering etcd snapshot programatically.
echo "> Sleeping 100s to allow an etcd snapshot to be created..."
sleep 100

echo "> Restoring the control plane Node..."
# TODO: Check why GRM gets deployed to worker Nodes
docker exec -ti gind-machine-0 gardenadm init -d /gardenadm/resources --recover --use-bootstrap-etcd
