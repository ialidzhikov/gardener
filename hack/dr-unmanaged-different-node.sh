#!/usr/bin/env bash

set -e

function targetMachine() {
  KUBECONFIG_SELFHOSTEDSHOOT_CLUSTER="$PWD/dev-setup/kubeconfigs/self-hosted-shoot/kubeconfig"
  ./hack/usage/generate-kubeconfig.sh self-hosted-shoot --docker gind-machine-0 > "$KUBECONFIG_SELFHOSTEDSHOOT_CLUSTER"
  export KUBECONFIG="$KUBECONFIG_SELFHOSTEDSHOOT_CLUSTER"
}

function triggerEtcdDeltaSnapshot() {
  # Trigger an etcd snapshot to flush the latest cluster state to the backup store.
  # etcd-backup-restore takes deltas on a schedule (every 5min by default), so without an
  # explicit trigger the bucket may not yet contain recent state (e.g. the gardenlet
  # Deployment created after `gardenadm connect`).
  # Trigger a delta (not a full) so the recovery path exercises full+delta replay, matching a real disaster.
  # /snapshot/delta blocks until the delta is uploaded.

  targetMachine
  ETCD_MAIN_POD=$(kubectl -n kube-system get pod -l app.kubernetes.io/name=etcd-main \
    -o jsonpath='{.items[0].metadata.name}')
  if [ -z "${ETCD_MAIN_POD}" ]; then
    echo "ERROR: could not find etcd-main pod in kube-system" >&2
    exit 1
  fi

  echo "> Sending HTTP request for a delta snapshot..."
  docker exec -ti gind-machine-0 curl -sk --fail "https://localhost:8080/snapshot/delta"
}

echo "> Setting up gind (machine containers only)..."
make gind-up SCENARIO=machines

echo "> Initializing control plane Node..."
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

echo "> Sanity checking that gardener-apiserver is running..."
VIRTUAL_GARDEN_KUBECONFIG="./dev-setup/kubeconfigs/virtual-garden/kubeconfig"
kubectl --kubeconfig "$VIRTUAL_GARDEN_KUBECONFIG" get namespaces

echo "> Building gardenadm binary..."
make -B gardenadm

echo "> Connecting the Shoot cluster to Gardener..."
CONNECT_COMMAND=$(KUBECONFIG="$VIRTUAL_GARDEN_KUBECONFIG" ./bin/gardenadm token create --print-connect-command --shoot-namespace=garden --shoot-name=root | tr -d '"')
docker exec -ti gind-machine-0 $(echo $CONNECT_COMMAND)

echo "> Obtaining a ShootState resource for the Shoot..."
# Patching the Shoot status with a successful last operation is required to allow the shootstate-controller to create a ShootState for the Shoot
echo "> Patching the Shoot status with a successful create lastOperation..."
kubectl --kubeconfig "$VIRTUAL_GARDEN_KUBECONFIG" -n garden patch shoot root --subresource status --type=merge --patch='{"status":{"lastOperation":{"type": "Create","state": "Succeeded"}}}'

# Rolling out the gardenlet Deployment is required to trigger the shootstate-controller to create a ShootState for the Shoot
echo "> Rolling out the kube-system/gardenlet Deployment to trigger ShootState creation..."
targetMachine
kubectl -n kube-system rollout restart deployment/gardenlet
echo "> Waiting until the kube-system/gardenlet Deployment successfully rolled out..."
kubectl -n kube-system rollout status deployment/gardenlet
echo "> Waiting until the ShootState is created..."
for i in {1..6}; do
  if kubectl --kubeconfig "$VIRTUAL_GARDEN_KUBECONFIG" -n garden get shootstate root &> /dev/null; then
    break
  fi
  echo "> Attempt $i/6: Waiting until garden/root ShootState is created. Sleeping 10s..."
  sleep 10
done

echo "> Triggering an etcd delta snapshot before simulating the disaster..."
triggerEtcdDeltaSnapshot

echo "> Simulating a disaster event..."
echo "> Stopping the gind-machine-0 container..."
docker stop gind-machine-0
echo "> Deleting the gind-machine-0 container with its volumes..."
docker rm --volumes gind-machine-0

echo "> Updating envoy.yaml to route apiserver traffic to gind-machine-3..."
sed -i 's/address: gind-machine-[0-9]*/address: gind-machine-3/g' dev-setup/gind/envoy.yaml
docker restart gind-apiserver-lb

echo "> Copying virtual garden kubeconfig to the gind-machine-3 container..."
docker cp ./dev-setup/kubeconfigs/virtual-garden/kubeconfig gind-machine-3:/virtual-garden-kubeconfig

echo "> Downloading Gardener configuration resources for the Shoot..."
docker exec -ti gind-machine-3 mkdir /gardenadm/discover-output
docker exec -ti gind-machine-3 gardenadm discover existing --name root --namespace garden --kubeconfig /virtual-garden-kubeconfig -d /gardenadm/discover-output
docker exec -ti gind-machine-3 rm /gardenadm/discover-output/lease-self-hosted-shoot-root.yaml

echo "> Preparing the etcd backup on the Node..."
backup_data_path=$(find dev/local-backupbuckets | grep v2$ | grep -v garden)
docker cp dev/local-backupbuckets gind-machine-3:/local-backupbuckets

echo "> Restoring the control plane Node..."
docker exec -ti gind-machine-3 gardenadm init -d /gardenadm/discover-output --recover --prior-node-name=gind-machine-0 --backup-data-path "/${backup_data_path#dev/}"

# For the purpose of the local setup, we delete the Master Lease records from ETCD post-restore to speed up the development process.
# Master leases are used for constructing an `EndpointSlice` for kuba-apiserver instances. During the restoration, the IP of the
# previously runnnig instance gets addded to the new endpoint slice, since it's master lease is still present in ETCD. Having this
# outdated IP within the `EndpointSlice` can cause connectivity issues since there is not instance for the [old] IP listed.
# Deleting all master leases, cleans up the redundant one(s) and at the same time creates up-to-date leases for the currently running
# kube-apiserver instance(s).
# To read more about the reason why we delete these leases, refer to https://github.com/kubernetes/kubernetes/issues/86812.
# TODO: Revisit the master lease deletion logic and validate if:
# - kube-proxy marks the IP in the EndpointSlice as unreachable and does not forward traffic to it.
# - master lease expires at some point and the EndpointSlice gets updates with the relevant ones during reconciliation
# - adding a "check" step within the init flow that will validate / perform the known ETCD data cleanup tasks
echo "> Installing ETCDCTL CLI tool"
docker exec -ti gind-machine-3 sh -c "apt-get update && apt-get install etcd-client"
echo "> Deleting Master Leases from ETCD"
docker exec -ti gind-machine-3 sh -c "ETCDCTL_API=3 etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/var/lib/static-pods/kube-apiserver/ca-etcd/bundle.crt --cert=/var/lib/static-pods/kube-apiserver/etcd-client/tls.crt --key=/var/lib/static-pods/kube-apiserver/etcd-client/tls.key del --prefix /registry/masterleases/"

echo "> Verifying the control plane Node restoration..."
./hack/dr-verify-restore.sh
