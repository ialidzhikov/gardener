// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package restore

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

	// Zone is the availability zone in which the new node is being initialized.
	// It is validated against the `.spec.provider.workers[].zones` field of the Shoot manifest.
	// If the worker pool has multiple zones configured, this flag is required.
	// If it has exactly one zone configured, that zone is automatically applied and the flag is optional.
	// If it has no zones configured, this flag must not be set.
	Zone string
	// BackupDataPath is the local path on the node where the etcd backup data is stored.
	// When set, the bootstrap etcd will be initialized from this path using the Local storage provider.
	// The path is expected to have the structure: <backupBucketsRoot>/<bucketName>/<namespace>--<uid>/etcd-main/v2
	BackupDataPath string
	// PriorNodeName defines the name of the node that is going to be replaced. Must be used alongside `--recover` to take effect.
	PriorNodeName string
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

	if o.BackupDataPath == "" {
		return fmt.Errorf("must provide --backup-data-path")
	}
	if o.PriorNodeName == "" {
		return fmt.Errorf("must provide --prior-node-name")
	}

	resources, err := gardenadm.ReadManifests(o.Log, os.DirFS(o.ConfigDir))
	if err != nil {
		return fmt.Errorf("failed loading resources for gardenadm restore validation: %w", err)
	}

	if resources.ShootState == nil {
		return fmt.Errorf("gardenadm restore requires a ShootState resource in the config directory, but none was found")
	}
	if err := validateShoot(resources.Shoot); err != nil {
		return err
	}
	if err := validateBackupResources(resources); err != nil {
		return err
	}

	effectiveZone, err := cmd.ValidateAndDetermineControlPlaneZone(resources.Shoot, o.Zone)
	if err != nil {
		return err
	}

	o.Zone = effectiveZone
	return nil
}

func validateShoot(shoot *gardencorev1beta1.Shoot) error {
	// .status.uid is required because it determines the BackupBucket and BackupEntry names. If it is empty,
	// `gardenadm init` would generate a fresh UID and the etcd snapshot in the existing bucket would be
	// orphaned, silently turning the "recover" into a fresh control plane bring-up.
	if shoot == nil || shoot.Status.UID == "" {
		return fmt.Errorf("gardenadm restore requires the Shoot manifest in the config directory to have .status.uid set " +
			"(this is the original Shoot UID and is needed to locate the existing BackupBucket and BackupEntry); " +
			"use 'gardenadm discover existing --name <name> --namespace <namespace>' to export the Shoot from the garden cluster")
	}

	return nil
}

// validateBackupResources cross-checks the BackupBucket/BackupEntry manifests in the config dir against the
// Shoot. When backup is configured for the Shoot, both manifests must be present (typically
// exported by `gardenadm discover`) and their names/bucket reference must match what would be computed from the
// Shoot, so that the recovered control plane points at the same backup location.
func validateBackupResources(resources gardenadm.Resources) error {
	if resources.BackupBucket == nil && resources.BackupEntry == nil {
		return fmt.Errorf("gardenadm restore requires both BackupBucket and BackupEntry manifests in the config directory " +
			"when backup is configured for the Shoot, but neither was found; " +
			"use 'gardenadm discover --shoot-name <name> --shoot-namespace <namespace>' to export them from the garden cluster")
	} else if resources.BackupBucket == nil {
		return fmt.Errorf("gardenadm restore requires a BackupBucket manifest in the config directory when backup is configured for the Shoot, but none was found")
	} else if resources.BackupEntry == nil {
		return fmt.Errorf("gardenadm restore requires a BackupEntry manifest in the config directory when backup is configured for the Shoot, but none was found")
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

// Complete completes the options.
func (o *Options) Complete() error {
	return o.ManifestOptions.Complete()
}

func (o *Options) addFlags(fs *pflag.FlagSet) {
	o.ManifestOptions.AddFlags(fs)
	fs.StringVarP(&o.Zone, "zone", "z", "", "Availability zone for the recovered node. Required if the control plane worker pool in the Shoot has multiple zones configured. Optional if exactly one zone is configured (applied automatically). Must not be set if no zones are configured.")
	fs.StringVar(&o.BackupDataPath, "backup-data-path", "", "Local path on the node where the etcd backup data is stored. Expected structure: <backupBucketsRoot>/<bucketName>/<namespace>--<uid>/etcd-main/v2")
	fs.StringVar(&o.PriorNodeName, "prior-node-name", "", "The name of the prior control plane node. Required in order to cleanup stale resources.")
}
