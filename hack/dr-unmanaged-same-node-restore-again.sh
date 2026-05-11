#!/usr/bin/env bash

set -euo pipefail

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

echo "> Restoring the control plane Node..."
docker exec -ti gind-machine-0 gardenadm init -d /gardenadm/resources --recover --use-bootstrap-etcd
