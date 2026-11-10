# `gardenadm restore`: Self-Hosted Shoot Control Plane Disaster Recovery

## Motivation

The control plane of a self-hosted Shoot cluster runs on a dedicated control plane Node.
If that control plane Node is lost on a disaster event, the cluster's brain is gone.
We must provide a way to restore the cluster state by restoring the control plane Node from the etcd backup.

## Solution

Introduce a new `gardenadm restore` command to restore a control plane from an etcd backup and manifests files.

## Usage

```bash
gardenadm restore \
    --backup-data-path /path/to/etcd-main/v2 \
    --config-dir /path/to/manifests \
    --prior-node-name <name>
```

Flags:
- `--backup-data-path` - a path on the host where the etcd backup is stored; a human operator must prepare the etcd backup on the host manually; in the future we might provide automation about it
- `--config-dir` - a path on the host where the Gardener manifests are stored (Shoot, CloudProfile, etc.); must be obtained by `gardenadm discover existing`.
- `--prior-node-name` - the name of the prior control plane node; required in order to cleanup stale resources.

## Demo

```
tree ./gardenadm-restore-demo/scripts/
./gardenadm-restore-demo/scripts/
├── 00-disable-system-components-on-worker-nodes.sh
├── 01-make-kind-and-gardener-up.sh
├── 02-make-gind-up.sh
├── 03-gardenadm-connect.sh
├── 04-create-precious-data.sh
├── 05-trigger-etcd-delta-snapshot.sh
├── 06-destroy-control-plane-node.sh
├── 07-recreate-control-plane-node.sh
├── 08-prepare-etcd-backup.sh
├── 09-gardenadm-discover-existing.sh
├── 10-gardenadm-restore.sh
├── 11-verify-gardenadm-restore.sh
└── common.sh

1 directory, 13 files
```

Retry:
```
make gind-down
./hack/gardenadm-restore-clean-orphaned-resources.sh
```

`gardenadm restore` takes around 8min in local setup.

## Status & What's next

- ✅ [unmanaged infrastructure, non-HA setup] Restore a control plane Node on new machine with the same hostname
- 🚧 [unmanaged infrastructure, non-HA setup] Restore a control plane Node on new machine with a different hostname
- 🔜 [managed infrastructure, non-HA setup] Restore a control plane Node
- ⛔️ / 🔜 [HA setup] Restore a control plane

✅: Done; 🚧: In progress; 🔜: Coming soon; ⛔️: Blocked by dependencies which have to be resolved/implemented first

## References

- Umbrella issue: https://github.com/gardener/gardener/issues/15264
- This demo branch: https://github.com/ialidzhikov/gardener/tree/demo/gardenadm-restore
