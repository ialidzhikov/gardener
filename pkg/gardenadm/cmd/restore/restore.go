// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package restore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	gardenadmbotanist "github.com/gardener/gardener/pkg/gardenadm/botanist"
	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	initcmd "github.com/gardener/gardener/pkg/gardenadm/cmd/init"
	"github.com/gardener/gardener/pkg/utils/flow"
	gardenletutils "github.com/gardener/gardener/pkg/utils/gardener/gardenlet"
)

// NewCommand creates a new cobra.Command.
func NewCommand(globalOpts *cmd.Options) *cobra.Command {
	opts := &Options{Options: globalOpts}

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a control plane node from an etcd backup and manifest files",
		Long: `Restore a control plane node from an etcd backup and manifest files. Use this command to recover the
self-hosted shoot cluster's control plane node after a disaster (e.g., the control plane node is lost)
onto a new or existing node.`,

		Example: `# Restore a control plane node from an etcd backup
gardenadm restore --config-dir /path/to/manifests --backup-data-path /path/to/etcd-main/v2 --prior-node-name <name>`,

		Args: cobra.NoArgs,

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.ParseArgs(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			if err := opts.Complete(); err != nil {
				return err
			}

			return run(cmd.Context(), opts)
		},
	}

	opts.addFlags(cmd.Flags())

	return cmd
}

func run(ctx context.Context, opts *Options) error {
	initOpts := &initcmd.Options{
		Options:         opts.Options,
		ManifestOptions: opts.ManifestOptions,
		// Restore requires an etcd backup, which only a control plane using etcd-druid (not the bootstrap etcd)
		// produces. Hence, restore always transitions to etcd-druid and does not expose --use-bootstrap-etcd.
		UseBootstrapEtcd: false,
		UseHostNetwork:   false,
		Zone:             opts.Zone,
	}

	// Track restore progress with a marker file so that a failed restore can be retried. The gardenlet deployment
	// recovered from the etcd snapshot would otherwise trip the guard in BootstrapControlPlane on a retry. We write the
	// marker before running the flow and remove it only on successful completion. While the marker exists, we reuse the
	// existing Force skip-path so the gardenlet check is skipped for this (retryable) restore.
	fs := afero.Afero{Fs: gardenadmbotanist.NewFs()}
	isRetry, err := prepareRestoreInProgressMarker(fs)
	if err != nil {
		return err
	}
	if isRetry {
		opts.Log.Info("Found restore-in-progress marker file, treating this invocation as a retry and skipping the existing-gardenlet check", "path", cmd.RestoreInProgressLocation)
		initOpts.Force = true
	}

	// The ETCD snapshot restored during `gardenadm restore` brings stale resources back to life which must be cleaned
	// up before the init flow re-reconciles the control plane. BootstrapControlPlane runs these cleanups (deleting the
	// prior control plane Node and its Pods, the stale gardener-node-agent OperatingSystemConfig Secret, and scaling the
	// gardenlet Deployment to zero) after the connection to the control plane is established and before the bootstrap
	// secrets are imported.
	b, err := initcmd.BootstrapControlPlane(ctx, initOpts, opts.BackupDataPath, opts.PriorNodeName, true)
	if err != nil {
		return fmt.Errorf("failed to bootstrap control plane: %w", err)
	}

	if err := runRestoreFlow(ctx, b, opts); err != nil {
		// Best-effort: restore gardenlet replicas even when the restore flow fails so the cluster is not left with
		// gardenlet permanently scaled to zero.
		if scaleErr := b.ScaleUpGardenlet(ctx, b.SeedClientSet.Client()); scaleErr != nil {
			opts.Log.Error(scaleErr, "Failed to scale gardenlet back up after restore flow failure")
		}
		return err
	}

	if err := b.ScaleUpGardenlet(ctx, b.SeedClientSet.Client()); err != nil {
		return fmt.Errorf("failed scaling gardenlet back up after restore: %w", err)
	}

	removeRestoreInProgressMarker(opts.Log, fs)

	fmt.Fprintf(opts.Out, `
Your Shoot cluster control-plane node has been restored successfully!

To complete the restoration, the Shoot must now be reconciled by gardenlet. Trigger a reconciliation by running
the following against the garden cluster:

  kubectl -n %s annotate shoot %s gardener.cloud/operation=reconcile
`, b.Shoot.GetInfo().Namespace, b.Shoot.GetInfo().Name)

	return nil
}

// runRestoreFlow runs the restore flow that brings up the restored control plane node. It deploys only the components
// required for the restored control plane node to become functional again. All other components are expected to be
// present in the restored etcd and are healed by a subsequent Shoot reconciliation by gardenlet.
func runRestoreFlow(ctx context.Context, b *gardenadmbotanist.GardenadmBotanist, opts *Options) error {
	dir := filepath.Dir(cmd.ConfigDirLocation)
	if err := b.FS.MkdirAll(dir, os.ModeDir); err != nil {
		return fmt.Errorf("failed creating config directory location dir %s: %w", dir, err)
	}
	if err := b.FS.WriteFile(cmd.ConfigDirLocation, []byte(opts.ConfigDir), 0640); err != nil {
		return fmt.Errorf("failed writing config directory location file %s: %w", cmd.ConfigDirLocation, err)
	}

	podNetworkAvailable, err := b.IsPodNetworkAvailable(ctx)
	if err != nil {
		return fmt.Errorf("failed checking whether pod network is already available: %w", err)
	}

	// If the self-hosted shoot is also the garden runtime cluster, then gardener-operator is taking over
	// responsibility of some components (e.g., etcd-druid). Detect this by checking whether a Garden resource exists.
	shootIsGarden, err := gardenletutils.ClusterIsGarden(ctx, b.SeedClientSet.Client())
	if err != nil {
		return fmt.Errorf("failed checking whether shoot is garden: %w", err)
	}

	var (
		g = flow.NewGraph("restore")

		_                           = g.AddGroup(b.DeployNamespacesTaskGroup())
		_                           = g.AddGroup(b.DeployCloudProviderSecretTaskGroup())
		_                           = g.AddGroup(b.ReconcileCustomResourceDefinitionsTaskGroup())
		_                           = g.AddGroup(b.ReconcileClusterResourceTaskGroup())
		initializeSecretsManagement = g.AddGroup(b.InitializeSecretsManagementTaskGroup())
		activateGardenerNodeAgent   = g.Add(flow.Task{
			Name:         "Activating gardener-node-agent",
			Fn:           b.ActivateGardenerNodeAgent,
			Dependencies: flow.NewTaskIDs(initializeSecretsManagement),
		})
		approveGardenerNodeAgentCSR = g.Add(flow.Task{
			Name:         "Approving gardener-node-agent client certificate signing request",
			Fn:           flow.TaskFn(b.ApproveNodeAgentCertificateSigningRequest).RetryUntilTimeout(2*time.Second, time.Minute),
			Dependencies: flow.NewTaskIDs(activateGardenerNodeAgent),
		})
		waitUntilControlPlaneNodeLabeled = g.Add(flow.Task{
			Name:         "Waiting until the control plane Node is labeled",
			Fn:           flow.TaskFn(b.CheckControlPlaneNodeLabeled).RetryUntilTimeout(2*time.Second, 2*time.Minute),
			SkipIf:       podNetworkAvailable,
			Dependencies: flow.NewTaskIDs(approveGardenerNodeAgentCSR),
		})
		_ = g.AddGroup(
			b.ReconcileRuntimeGardenerResourceManagerTaskGroup(podNetworkAvailable, shootIsGarden, false).
				WithDependencies(approveGardenerNodeAgentCSR, waitUntilControlPlaneNodeLabeled),
		)
		_ = g.AddGroup(
			b.ReconcileGardenerResourceManagerTaskGroup(podNetworkAvailable, false).
				WithDependencies(approveGardenerNodeAgentCSR, waitUntilControlPlaneNodeLabeled),
		)
		_ = g.AddGroup(b.ReconcileReferencedResourcesTaskGroup())
		_ = g.AddGroup(b.ReconcileSystemResourcesTaskGroup())
		_ = g.AddGroup(b.ReconcileExtensionControllersTaskGroup(podNetworkAvailable).
			WithDependencies(waitUntilControlPlaneNodeLabeled))
		_ = g.AddGroup(
			b.ReconcileInfrastructureTaskGroup(false).
				WithDependencies(gardenadmbotanist.TaskGroupReconcileExtensionControllers),
		)
		_ = g.AddGroup(b.ReconcileShootNamespacesTaskGroup(false))
	)

	if err := g.Compile().Run(ctx, flow.Opts{
		Log:              opts.Log,
		ProgressReporter: flow.NewCommandLineProgressReporter(opts.ErrOut),
	}); err != nil {
		return flow.Errors(err)
	}

	return nil
}

// prepareRestoreInProgressMarker ensures the restore-in-progress marker file exists. It returns true if the marker
// already existed before this invocation, indicating that this is a retry and the existing-gardenlet check should be
// skipped. Its presence is the only signal; the file content is not used.
func prepareRestoreInProgressMarker(fs afero.Afero) (bool, error) {
	markerExists, err := fs.Exists(cmd.RestoreInProgressLocation)
	if err != nil {
		return false, fmt.Errorf("failed checking whether restore-in-progress marker file %s exists: %w", cmd.RestoreInProgressLocation, err)
	}
	if markerExists {
		return true, nil
	}

	dir := filepath.Dir(cmd.RestoreInProgressLocation)
	if err := fs.MkdirAll(dir, cmd.RestoreInProgressDirMode); err != nil {
		return false, fmt.Errorf("failed creating directory %s for restore-in-progress marker file: %w", dir, err)
	}

	if err := fs.WriteFile(cmd.RestoreInProgressLocation, nil, cmd.RestoreInProgressFileMode); err != nil {
		return false, fmt.Errorf("failed writing restore-in-progress marker file %s: %w", cmd.RestoreInProgressLocation, err)
	}
	return false, nil
}

// removeRestoreInProgressMarker removes the restore-in-progress marker file. Only this final restore task removes the
// marker, so it is expected to exist at this point. Any removal error (including the file already being gone) is logged
// as a warning but does not fail the restore, since the restore itself has already succeeded at this point.
func removeRestoreInProgressMarker(log logr.Logger, fs afero.Afero) {
	err := fs.Remove(cmd.RestoreInProgressLocation)
	if err != nil {
		if errors.Is(err, afero.ErrFileNotFound) {
			log.Info("Warning: restore-in-progress marker file was already absent at completion; something removed it out of band", "path", cmd.RestoreInProgressLocation)
			return
		}
		log.Info("Warning: failed to remove restore-in-progress marker file; the restore succeeded, but a leftover marker will cause the next invocation to be treated as a retry", "path", cmd.RestoreInProgressLocation, "err", err.Error())
	}
}
