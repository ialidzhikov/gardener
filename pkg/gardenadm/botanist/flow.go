// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package botanist

import (
	"context"
	"time"

	"github.com/gardener/gardener/pkg/client/kubernetes"
	"github.com/gardener/gardener/pkg/gardenlet/operation/botanist"
	"github.com/gardener/gardener/pkg/utils/flow"
)

// TaskGroupCleanupStaleRestoreResources is a flow.TaskID for a logical flow.TaskGroup.
const TaskGroupCleanupStaleRestoreResources flow.TaskID = "TaskGroupCleanupStaleRestoreResources"

// CleanupStaleRestoreResourcesTaskGroup returns the flow.TaskGroup that cleans up the stale resources restored from the
// ETCD snapshot during `gardenadm restore`: it finalizes and deletes the restored ManagedResources, deletes the stale
// gardener-node-agent OperatingSystemConfig Secret, and force-deletes the prior control plane Node together with its Pods.
//
// clientSet is a pointer because the control plane client set is not yet initialized while the graph is being built; the
// tasks dereference it only from within their Fn (i.e. at flow run time), after the connection has been established.
func (b *GardenadmBotanist) CleanupStaleRestoreResourcesTaskGroup(clientSet *kubernetes.Interface, priorNodeName string) flow.TaskGroup {
	g := flow.NewTaskGroup(TaskGroupCleanupStaleRestoreResources)

	// FinalizeManagedResources must run before DeleteStaleOperatingSystemConfigSecret: the OperatingSystemConfig Secret
	// is managed by the `shoot-gardener-node-agent` ManagedResource, whose finalizers must be gone before the Secret can
	// be deleted and MigrateSecrets can reinstall the bootstrap-content Secret under the same name.
	finalizeManagedResources := g.Add(flow.Task{
		Name: "Finalizing and deleting ManagedResources restored from the ETCD snapshot",
		Fn: func(ctx context.Context) error {
			return b.FinalizeManagedResources(ctx, (*clientSet).Client())
		},
	})
	_ = g.Add(flow.Task{
		Name: "Deleting stale gardener-node-agent OperatingSystemConfig Secret restored from the ETCD snapshot",
		Fn: func(ctx context.Context) error {
			return b.DeleteStaleOperatingSystemConfigSecret(ctx, (*clientSet).Client())
		},
		Dependencies: flow.NewTaskIDs(finalizeManagedResources),
	})
	// Deleting the prior control plane Node and its Pods is independent of the ManagedResource/Secret cleanup.
	_ = g.Add(flow.Task{
		Name: "Deleting the prior control plane Node and the Pods running on it",
		Fn: func(ctx context.Context) error {
			return b.DeletePriorNodeAndPodsRunningOnIt(ctx, (*clientSet).Client(), priorNodeName)
		},
	})

	return g
}

// TaskGroupReconcileExtensionControllers is a flow.TaskID for a logical flow.TaskGroup.
const TaskGroupReconcileExtensionControllers flow.TaskID = "TaskGroupReconcileExtensionControllers"

// ReconcileExtensionControllersTaskGroup returns the flow.TaskGroup for deploying the extension controllers and waiting
// for their readiness. If podNetworkAvailable is true, the deployment reconciles the controllers into the pod network.
func (b *GardenadmBotanist) ReconcileExtensionControllersTaskGroup(podNetworkAvailable bool) flow.TaskGroup {
	var (
		g = flow.NewTaskGroup(TaskGroupReconcileExtensionControllers).WithDependencies(botanist.TaskGroupReconcileGardenerResourceManager)

		deployExtensionControllers = g.Add(flow.Task{
			Name: "Deploying extension controllers",
			Fn: flow.TaskFn(func(ctx context.Context) error {
				return b.ReconcileExtensionControllerInstallations(ctx, !podNetworkAvailable)
			}).RetryUntilTimeout(5*time.Second, 30*time.Second),
		})
		_ = g.Add(flow.Task{
			Name:         "Waiting until extension controllers report readiness",
			Fn:           b.WaitUntilExtensionControllerInstallationsHealthy,
			Dependencies: flow.NewTaskIDs(deployExtensionControllers),
		})
	)

	return g
}

// TaskGroupReconcileNetworkPolicies is a flow.TaskID for a logical flow.TaskGroup.
const TaskGroupReconcileNetworkPolicies flow.TaskID = "TaskGroupReconcileNetworkPolicies"

// ReconcileNetworkPoliciesTaskGroup returns the flow.TaskGroup for reconciling the network policies.
func (b *GardenadmBotanist) ReconcileNetworkPoliciesTaskGroup() flow.TaskGroup {
	return flow.NewTaskGroup(TaskGroupReconcileNetworkPolicies, flow.Task{
		Name: "Deploying network policies",
		Fn:   b.ApplyNetworkPolicies,
	}).WithDependencies(botanist.TaskGroupReconcileGardenerResourceManager, TaskGroupReconcileExtensionControllers)
}
