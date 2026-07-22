#!/usr/bin/env bash

set -euo pipefail

echo "> Simulating a disaster event..."
echo "> Stopping the gind-machine-0 container..."
docker stop gind-machine-0
echo "> Deleting the gind-machine-0 container with its volumes..."
docker rm --volumes gind-machine-0

echo "> Setting up gind (recreating the gind-machine-0 container)..."
make gind-up SCENARIO=machines

echo "> Copying virtual garden kubeconfig to the gind-machine-0 container..."
docker cp ./dev-setup/kubeconfigs/virtual-garden/kubeconfig gind-machine-0:/virtual-garden-kubeconfig

echo "> Downloading Gardener configuration resources for the Shoot..."
docker exec -ti gind-machine-0 mkdir /gardenadm/discover-output
docker exec -ti gind-machine-0 gardenadm discover existing --name root --namespace garden --kubeconfig /virtual-garden-kubeconfig -d /gardenadm/discover-output
docker exec -ti gind-machine-0 rm /gardenadm/discover-output/lease-self-hosted-shoot-root.yaml

echo "> Preparing the etcd backup on the Node..."
backup_data_path=$(find dev/local-backupbuckets | grep v2$ | grep -v garden)
docker cp dev/local-backupbuckets gind-machine-0:/local-backupbuckets

echo "> Restoring the control plane Node..."
docker exec -ti gind-machine-0 gardenadm restore -d /gardenadm/discover-output --prior-node-name=gind-machine-0 --backup-data-path "/${backup_data_path#dev/}"

echo "> Verifying the control plane Node restoration..."
./hack/dr-verify-restore.sh
