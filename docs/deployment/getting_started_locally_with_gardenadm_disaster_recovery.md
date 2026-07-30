# Disaster Recovery for Self-Hosted Shoot Clusters Locally

> [!CAUTION]
> The `gardenadm` tool is currently under development and considered highly experimental.
> Do not use it in production environments.
> Read more about it in [GEP-0028](https://github.com/gardener/enhancements/tree/main/geps/0028-self-hosted-shoot-clusters).

This document walks you through simulating and recovering from a control plane disaster for a [Self-Hosted Shoot Cluster](../concepts/gardenadm.md) on your local machine.
It builds on top of the ["unmanaged infrastructure" scenario](getting_started_locally_with_gardenadm.md#unmanaged-infrastructure-scenario) and the [connection of the Self-Hosted Shoot to Gardener](getting_started_locally_with_gardenadm.md#connecting-the-self-hosted-shoot-cluster-to-gardener).

If you encounter difficulties, please open an issue so that we can make this process easier.

## Overview

A Self-Hosted Shoot Cluster runs its own control plane on its machines.
The control plane's etcd is continuously backed up (full and delta snapshots) to a backup store.
When the control plane Node is lost (the "disaster"), the control plane can be recovered on a new Node from these backups plus the Gardener configuration resources (`Shoot`, `ShootState`, `BackupBucket`, ...) stored in the garden cluster.

The local setup simulates this end to end:

1. A [KinD](https://kind.sigs.k8s.io/) cluster hosts the Gardener control plane (the "virtual garden").
1. A set of [Gardener-in-Docker (gind)](getting_started_locally_with_gardenadm.md#unmanaged-infrastructure-scenario) machine containers act as the Self-Hosted Shoot's Nodes.
1. The Self-Hosted Shoot is connected to Gardener, an etcd snapshot is taken, and then the control plane Node is destroyed.
1. The control plane is recovered on a Node and the recovery is verified.

Two recovery scenarios are provided:

- **Same Node**: the control plane is restored onto a freshly recreated `gind-machine-0` (i.e., the same Node name as before the disaster).
- **Different Node**: the control plane is restored onto a different Node (`gind-machine-3`), which additionally re-routes the API server load balancer and cleans up stale master leases in etcd.

## Prerequisites

- Make sure that you have followed the [Prerequisites of the `gardenadm` local setup](getting_started_locally_with_gardenadm.md#prerequisites).

## Reusing the KinD Cluster and the Gardener Control Plane

The KinD cluster and the Gardener control plane running on it are **provisioned once** and **reused across disaster recovery runs**.
This avoids recreating the KinD cluster and redeploying Gardener on every run, which is time-consuming.

Provision them with:

```shell
./hack/dr-kind-connect-up.sh
```

This runs `make kind-up` and `make gardenadm-up SCENARIO=connect-kind`, and creates the virtual garden kubeconfig at `./dev-setup/kubeconfigs/virtual-garden/kubeconfig`.
The disaster recovery scripts require this kubeconfig to exist and to be usable; they fail early with a hint if it is missing or the control plane is not reachable.

> [!NOTE]
> You can point the scripts at a different virtual garden kubeconfig by setting the `VIRTUAL_GARDEN_KUBECONFIG` environment variable.

## Running Disaster Recovery

Once the KinD cluster and the Gardener control plane are up, run one of the scenarios:

```shell
# Recover the control plane on the same Node (gind-machine-0):
./hack/dr-unmanaged-same-node.sh

# Recover the control plane on a different Node (gind-machine-3):
./hack/dr-unmanaged-different-node.sh
```

Each script performs the full flow:

1. Sets up the gind machine containers and initializes the control plane Node (`gardenadm init`).
1. Joins the worker Nodes and creates a dummy workload used later to verify the recovery.
1. Connects the Self-Hosted Shoot to Gardener (`gardenadm connect`) and waits for the `ShootState` to be created.
1. Triggers an etcd delta snapshot so the latest cluster state is flushed to the backup store.
1. Simulates the disaster by stopping and removing the control plane Node's container.
1. Recovers the control plane on the target Node (`gardenadm discover existing` + `gardenadm restore`).
1. Verifies the recovery via `./hack/dr-verify-restore.sh` (the dummy workload and the Shoot UID survived).

On success, you will see:

```text
🎉 Success! The control plane Node was successfully restored!
```

## Restoring the Control Plane Again (Same Node)

When iterating on the restore path itself, re-running the full `hack/dr-unmanaged-same-node.sh` flow is unnecessarily slow: it recreates the machine containers, initializes the control plane, joins the workers and connects the Self-Hosted Shoot to Gardener from scratch.

`hack/dr-unmanaged-same-node.sh` leaves behind a fully set-up and connected Self-Hosted Shoot (its `Shoot`, `ShootState`, `BackupBucket` and etcd backups still exist).
`hack/dr-unmanaged-same-node-restore-again.sh` reuses that state and runs **only** the disaster-and-restore portion of the flow on `gind-machine-0`:

```shell
./hack/dr-unmanaged-same-node-restore-again.sh
```

It simulates the disaster (stops and removes `gind-machine-0`), recreates the Node, downloads the Gardener configuration resources (`gardenadm discover existing`), restores the control plane from the existing etcd backups (`gardenadm restore --prior-node-name=gind-machine-0`) and verifies the recovery via `./hack/dr-verify-restore.sh`.

> [!NOTE]
> This script assumes a Self-Hosted Shoot that was already set up and connected by a prior `hack/dr-unmanaged-same-node.sh` run, together with the reused KinD cluster and Gardener control plane.
> Do not run `make gind-down` or `./hack/dr-clean-orphaned-resources.sh` before it - those remove exactly the state it relies on.
> It can be run repeatedly to restore the control plane on `gind-machine-0` again and again.

## Cleaning Up Between Runs

A completed run leaves two things behind that must be cleaned up before starting another run:

- The Self-Hosted Shoot cluster, i.e. the gind machine containers, which the disaster recovery scripts do not tear down at the end.
- The resources created in the reused virtual garden cluster during the run (`Shoot`, `ShootState`, `BackupEntry`, `BackupBucket`) as well as the on-disk backup data.

The disaster recovery scripts **fail early** if a `Shoot garden/root` from a previous run still exists, to avoid colliding with those resources.

Clean up before starting another run:

```shell
# 1. Destroy the Self-Hosted Shoot cluster (the gind machine containers):
make gind-down

# 2. Clean up the orphaned Gardener resources and on-disk backup data:
./hack/dr-clean-orphaned-resources.sh
```

`make gind-down` tears down the gind machine containers. Because a KinD cluster is still present, it leaves the shared infrastructure (DNS, registry, backup buckets, `kind` Docker network) intact so it can keep serving the reused KinD cluster and the Gardener control plane.

`./hack/dr-clean-orphaned-resources.sh` then cleans up what `make gind-down` does not - the resources created in the virtual garden cluster during the run:

- Force-deletes the `Shoot`, `ShootState`, `BackupEntry`, and (non-`garden-*`) `BackupBucket` resources of the Self-Hosted Shoot from the virtual garden cluster.
- Removes the on-disk local backup bucket data of the Self-Hosted Shoot under `./dev/local-backupbuckets`, while keeping the `garden-*` bucket of the virtual garden control plane.

> [!NOTE]
> The gardenlet that owns the finalizers on these resources runs on the control plane Node, which is destroyed during the disaster simulation.
> No controller is left to process a graceful deletion, so `dr-clean-orphaned-resources.sh` force-removes the finalizers.
> This is only safe in this local development and test flow.

After cleaning up, you can run a disaster recovery scenario again without recreating the KinD cluster or the Gardener control plane.

## Tearing Down the Setup

To tear down the whole setup, destroy both the Self-Hosted Shoot cluster and the KinD cluster with the Gardener control plane:

```shell
# Destroy the Self-Hosted Shoot cluster (the gind machine containers):
make gind-down

# Destroy the KinD cluster and the Gardener control plane:
make kind-down
```
