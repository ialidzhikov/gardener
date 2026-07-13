// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package init

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"

	v1beta1helper "github.com/gardener/gardener/pkg/api/core/v1beta1/helper"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/gardener/gardener/pkg/gardenadm"
	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
)

// Options contains options for this command.
type Options struct {
	*cmd.Options
	cmd.ManifestOptions

	// UseBootstrapEtcd indicates whether to use the bootstrap etcd instead of transitioning to etcd-druid
	// (default: false). This helps `gardenadm init` to run faster.
	UseBootstrapEtcd bool
	// UseHostNetwork indicates whether to run gardener-resource-manager and extensions in host network (instead of
	// redeploying them into the pod network after bootstrapping) (default: false). This helps `gardenadm init` to run
	// faster.
	UseHostNetwork bool
	// Zone is the availability zone in which the new node is being initialized.
	// It is validated against the `.spec.provider.workers[].zones` field of the Shoot manifest.
	// If the worker pool has multiple zones configured, this flag is required.
	// If it has exactly one zone configured, that zone is automatically applied and the flag is optional.
	// If it has no zones configured, this flag must not be set.
	Zone string

	Recover bool

	// PriorNodeName defines the name of the node that is going to be replaced. Must be used alongside `--recover` to take effect.
	PriorNodeName string
	// BackupDataPath is the local path on the node where the etcd backup data is stored.
	// When set, the bootstrap etcd will be initialized from this path using the Local storage provider.
	// The path is expected to have the structure: <backupBucketsRoot>/<bucketName>/<namespace>--<uid>/etcd-main/v2
	BackupDataPath string
}

// ParseArgs parses the arguments to the options.
func (o *Options) ParseArgs(args []string) error {
	return o.ManifestOptions.ParseArgs(args)
}

// Validate validates the options.
func (o *Options) Validate() error {
	if err := o.ManifestOptions.Validate(); err != nil {
		return err
	}

	if err := o.validateFlagCombinations(); err != nil {
		return err
	}

	return o.validateZone()
}

func (o *Options) validateFlagCombinations() error {
	if o.Recover {
		resources, err := gardenadm.ReadManifests(o.Log, os.DirFS(o.ConfigDir))
		if err != nil {
			return fmt.Errorf("failed loading resources for recover validation: %w", err)
		}

		if o.PriorNodeName == "" {
			return fmt.Errorf("--recover must be combined with --prior-node-name")
		}

		if err := validateShootState(resources); err != nil {
			return err
		}

		if err := validateBackupResources(resources); err != nil {
			return err
		}
	}

	if o.PriorNodeName != "" && !o.Recover {
		return fmt.Errorf("--prior-node-name must be combined with --recover")
	}

	if o.BackupDataPath != "" && !o.Recover {
		return fmt.Errorf("--backup-data-path must be combined with --recover")
	}

	if o.Recover && o.BackupDataPath == "" {
		return fmt.Errorf("--recover must be combined with --backup-data-path")
	}

	return nil
}

// validateShootState ensures the config directory contains the inputs required for a `--recover` run: a ShootState
// resource, and a Shoot manifest carrying its original `.status.uid`. The UID determines the BackupBucket and
// BackupEntry names, so without it `gardenadm init` would generate a fresh UID and silently turn the recovery into a
// fresh control plane bring-up, orphaning the etcd snapshot in the existing bucket.
func validateShootState(resources gardenadm.Resources) error {
	if resources.ShootState == nil {
		return fmt.Errorf("--recover requires a ShootState resource in the config directory, but none was found")
	}

	// .status.uid is required because it determines the BackupBucket and BackupEntry names. If it is empty,
	// `gardenadm init` would generate a fresh UID and the etcd snapshot in the existing bucket would be
	// orphaned, silently turning the "recover" into a fresh control plane bring-up.
	if resources.Shoot == nil || resources.Shoot.Status.UID == "" {
		return fmt.Errorf("--recover requires the Shoot manifest in the config directory to have .status.uid set " +
			"(this is the original Shoot UID and is needed to locate the existing BackupBucket and BackupEntry); " +
			"use 'gardenadm discover existing --name <name> --namespace <namespace>' to export the Shoot from the garden cluster")
	}

	return nil
}

// validateRecoverBackupResources cross-checks the BackupBucket/BackupEntry manifests in the config dir against the
// Shoot when `--recover` is set. When backup is configured for the Shoot, both manifests must be present (typically
// exported by `gardenadm discover`) and their names/bucket reference must match what would be computed from the
// Shoot, so that the recovered control plane points at the same backup location.
func validateBackupResources(resources gardenadm.Resources) error {
	if resources.BackupBucket == nil && resources.BackupEntry == nil {
		return fmt.Errorf("--recover requires both BackupBucket and BackupEntry manifests in the config directory " +
			"when backup is configured for the Shoot, but neither was found; " +
			"use 'gardenadm discover --shoot-name <name> --shoot-namespace <namespace>' to export them from the garden cluster")
	} else if resources.BackupBucket == nil {
		return fmt.Errorf("--recover requires a BackupBucket manifest in the config directory when backup is configured for the Shoot, but none was found")
	} else if resources.BackupEntry == nil {
		return fmt.Errorf("--recover requires a BackupEntry manifest in the config directory when backup is configured for the Shoot, but none was found")
	}

	expectedBackupBucketName := string(resources.Shoot.Status.UID)
	if resources.BackupBucket.Name != expectedBackupBucketName {
		return fmt.Errorf("BackupBucket manifest name %q does not match the expected name %q (= Shoot .status.uid)", resources.BackupBucket.Name, expectedBackupBucketName)
	}

	controlPlaneNamespace := v1beta1helper.ControlPlaneNamespaceForShoot(resources.Shoot)
	expectedBackupEntryName, err := gardenerutils.GenerateBackupEntryName(controlPlaneNamespace, resources.Shoot.Status.UID, resources.Shoot.UID)
	if err != nil {
		return fmt.Errorf("failed computing expected BackupEntry name: %w", err)
	}
	if resources.BackupEntry.Name != expectedBackupEntryName {
		return fmt.Errorf("BackupEntry manifest name %q does not match the expected name %q (= <controlPlaneNamespace>--<Shoot .status.uid>)", resources.BackupEntry.Name, expectedBackupEntryName)
	}

	if resources.BackupEntry.Spec.BucketName != resources.BackupBucket.Name {
		return fmt.Errorf("BackupEntry manifest .spec.bucketName %q does not match the BackupBucket manifest name %q", resources.BackupEntry.Spec.BucketName, resources.BackupBucket.Name)
	}

	return nil
}

// validateZone validates the zone configuration against the shoot specification.
func (o *Options) validateZone() error {
	resources, err := gardenadm.ReadManifests(o.Log, os.DirFS(o.ConfigDir))
	if err != nil {
		return fmt.Errorf("failed loading resources for zone validation: %w", err)
	}

	if v1beta1helper.HasManagedInfrastructure(resources.Shoot) {
		if o.Zone != "" {
			return fmt.Errorf("zone can't be configured for shoot with managed infrastructure")
		}
		return nil
	}

	if resources.Shoot == nil {
		return fmt.Errorf("zone validation failed shoot resource is missing in the manifests")
	}

	// init command is only for control plane node, therefore we look for the control plane pool
	var controlPlanePool *gardencorev1beta1.Worker
	if controlPlanePool = v1beta1helper.ControlPlaneWorkerPoolForShoot(resources.Shoot.Spec.Provider.Workers); controlPlanePool == nil {
		return fmt.Errorf("zone validation failed, shoot doesn't have a control plane worker pool configured")
	}

	effectiveZone, err := cmd.DetermineZone(*controlPlanePool, o.Zone)
	if err != nil {
		return fmt.Errorf("failed determining zone for control plane worker pool %q: %w", controlPlanePool.Name, err)
	}

	o.Zone = effectiveZone
	return nil
}

// Complete completes the options.
func (o *Options) Complete() error {
	return o.ManifestOptions.Complete()
}

func (o *Options) addFlags(fs *pflag.FlagSet) {
	o.ManifestOptions.AddFlags(fs)
	fs.BoolVar(&o.UseBootstrapEtcd, "use-bootstrap-etcd", false, "If set, the control plane continues using the bootstrap etcd instead of transitioning to etcd-druid. This can be useful for testing purposes to save time.")
	fs.BoolVar(&o.UseHostNetwork, "use-host-network", false, "If set, gardener-resource-manager and extensions continue to run in host network instead of getting redeployed into the pod network after bootstrapping. This can be useful for testing purposes to save time.")
	fs.StringVarP(&o.Zone, "zone", "z", "", "Availability zone for the new node. Required if the control plane worker pool in the Shoot has multiple zones configured. Optional if exactly one zone is configured (applied automatically). Must not be set if no zones are configured.")
	fs.BoolVar(&o.Recover, "recover", false, "If set, run control plane recovery flow.")
	fs.StringVar(&o.PriorNodeName, "prior-node-name", "", "The name of the prior control plane node. Required in order to cleanup stale resources. Must be used alongside `--recover` to take effect.")
	fs.StringVar(&o.BackupDataPath, "backup-data-path", "", "Local path on the node where the etcd backup data is stored. Expected structure: <backupBucketsRoot>/<bucketName>/<namespace>--<uid>/etcd-main/v2")
}
