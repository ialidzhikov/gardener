#!/usr/bin/env bash

set -euo pipefail

# Update the gardenadm binary and its image vector overwrite
make gardenadm-up

# Nuke machine but retain IP address
machine_pod=$(docker exec -it gardener-operator-local-control-plane crictl pods | grep machine-0 | cut -d ' ' -f 1)
machine_container=$(docker exec -it gardener-operator-local-control-plane crictl ps | grep "$machine_pod" | cut -d ' ' -f 1)
docker exec -it gardener-operator-local-control-plane crictl stop "$machine_container"
sleep 100

# Recovery (bootstrap + prep + second phase)
kubectl -n gardenadm-unmanaged-infra exec -it machine-0 -- gardenadm init -d /gardenadm/resources --recover --use-bootstrap-etcd
