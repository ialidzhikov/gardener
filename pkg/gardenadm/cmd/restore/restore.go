// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package restore

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/gardenadm/botanist"
	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	initcmd "github.com/gardener/gardener/pkg/gardenadm/cmd/init"
	kubernetesutils "github.com/gardener/gardener/pkg/utils/kubernetes"
)

func NewCommand(globalOpts *cmd.Options) *cobra.Command {
	opts := &Options{Options: globalOpts}

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a control plane node from an etcd backup and manifest files",
		Long: `Restore a control plane node from an etcd backup and manifest files. Use this command to recover the
self-hosted shoot cluster's control plane node after a disaster (e.g., the control plane node is lost)
onto a new or existing node.`,

		Example: `# Restore a control plane node from an etcd backup
gardenadm restore --config-dir /path/to/manifests --prior-node-name <name> --backup-data-path /path/to/etcd-main/v2`,

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

	b, err := initcmd.BootstrapControlPlane(ctx, initOpts, opts.BackupDataPath)
	if err != nil {
		return fmt.Errorf("failed to bootstrap control plane (1st recovery phase): %w", err)
	}

	if err := performRequiredCleanups(ctx, b, opts.PriorNodeName); err != nil {
		return fmt.Errorf("failed performing required cleanups: %w", err)
	}

	return initcmd.RunInitFlow(ctx, initOpts, b)
}

func performRequiredCleanups(ctx context.Context, b *botanist.GardenadmBotanist, priorNodeName string) error {
	b.Logger.Info("Performing required cleanups")

	if err := finalizeManagedResources(ctx, b.SeedClientSet.Client(), b.Logger); err != nil {
		return fmt.Errorf("failed to finalize ManagedResources: %w", err)
	}

	if err := deletePriorNodeAndPodsRunningOnIt(ctx, b.SeedClientSet.Client(), priorNodeName, b.Logger); err != nil {
		return fmt.Errorf("failed to delete prior Node and Pods running on it: %w", err)
	}

	// The OperatingSystemConfig secret restored from the etcd snapshot carries the managed etcd static-pod manifests.
	// Overwrite it with the bootstrap etcd content (built during the bootstrap phase) so that gardener-node-agent's
	// first reconciliation deploys the bootstrap etcd instead of prematurely materializing the managed etcd during the
	// bootstrap phase. See botanist.OverwriteOperatingSystemConfigSecret for the full rationale.
	if bootstrapSecret := b.OperatingSystemConfigSecret(); bootstrapSecret != nil {
		b.Logger.Info("Overwriting gardener-node-agent OperatingSystemConfig secret with bootstrap etcd content", "secret", client.ObjectKeyFromObject(bootstrapSecret))
		if err := b.OverwriteOperatingSystemConfigSecret(ctx, bootstrapSecret); err != nil {
			return fmt.Errorf("failed overwriting OperatingSystemConfig secret with bootstrap etcd content: %w", err)
		}
	}

	b.Logger.Info("Finished required cleanups")

	return nil
}

func finalizeManagedResources(ctx context.Context, c client.Client, logger logr.Logger) error {
	managedResourceList := &resourcesv1alpha1.ManagedResourceList{}
	if err := c.List(ctx, managedResourceList); err != nil {
		return fmt.Errorf("failed listing ManagedResources: %w", err)
	}

	for _, managedResource := range managedResourceList.Items {
		obj := managedResource.DeepCopy()
		obj.SetFinalizers(nil)

		logger.Info("Removing ManagedResource finalizers", "managedResource", client.ObjectKeyFromObject(obj))
		if err := c.Update(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed updating ManagedResource %s: %w", client.ObjectKeyFromObject(obj), err)
		}

		logger.Info("Deleting ManagedResource", "managedResource", client.ObjectKeyFromObject(obj))
		if err := c.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting ManagedResource %s: %w", client.ObjectKeyFromObject(obj), err)
		}
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	logger.Info("Waiting for ManagedResources to be cleaned up")
	if err := kubernetesutils.WaitUntilResourcesDeleted(ctxWithTimeout, c, managedResourceList, 10*time.Second); err != nil {
		return fmt.Errorf("failed waiting until ManagedResources are cleaned up: %w", err)
	}

	return nil
}

func deletePriorNodeAndPodsRunningOnIt(ctx context.Context, c client.Client, priorNodeName string, logger logr.Logger) error {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: priorNodeName}}

	logger.Info("Deleting Node", "node", client.ObjectKeyFromObject(node))
	if err := c.Delete(ctx, node); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed deleting Node %s: %w", client.ObjectKeyFromObject(node), err)
	}

	podList := &corev1.PodList{}
	if err := c.List(ctx, podList); err != nil {
		return fmt.Errorf("failed listing Pods: %w", err)
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != priorNodeName {
			continue
		}

		logger.Info("Force deleting Pod", "pod", client.ObjectKeyFromObject(&pod), "nodeName", pod.Spec.NodeName)
		options := &client.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0), PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)}
		if err := c.Delete(ctx, pod.DeepCopy(), options); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed force deleting Pod %s: %w", client.ObjectKeyFromObject(&pod), err)
		}
	}

	return nil
}
