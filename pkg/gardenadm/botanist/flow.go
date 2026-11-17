// SPDX-FileCopyrightText: Contributors to the Gardener project
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

// TaskGroupCleanUpStaleRestoredResources is a flow.TaskID for a logical flow.TaskGroup.
const TaskGroupCleanUpStaleRestoredResources flow.TaskID = "TaskGroupCleanUpStaleRestoredResources"

// CleanUpStaleRestoredResourcesTaskGroup returns the flow.TaskGroup that cleans up the stale resources restored from the
// ETCD snapshot during `gardenadm restore`.
//
// clientSet is a pointer because the control plane client set is not yet initialized while the graph is being built; the
// tasks dereference it only from within their Fn (i.e. at flow run time), after the connection has been established.
func (b *GardenadmBotanist) CleanUpStaleRestoredResourcesTaskGroup(clientSet *kubernetes.Interface, priorNodeName string) flow.TaskGroup {
	g := flow.NewTaskGroup(TaskGroupCleanUpStaleRestoredResources)

	finalizeGNAManagedResource := g.Add(flow.Task{
		Name: "Finalize gardener-node-agent ManagedResource",
		Fn: func(ctx context.Context) error {
			return b.FinalizeGardenerNodeAgentManagedResource(ctx, (*clientSet).Client())
		},
	})
	// The whole group must run before importSecrets so that the following MigrateSecrets task reinstalls the
	// bootstrap-content OperatingSystemConfig Secret under the same name (see DeleteStaleOperatingSystemConfigSecret).
	// This ordering is enforced by the group's consumer (BootstrapControlPlane), which makes importSecrets depend on it.
	_ = g.Add(flow.Task{
		Name: "Deleting stale gardener-node-agent OperatingSystemConfig Secret",
		Fn: func(ctx context.Context) error {
			return b.DeleteStaleOperatingSystemConfigSecret(ctx, (*clientSet).Client())
		},
		Dependencies: flow.NewTaskIDs(finalizeGNAManagedResource),
	})
	deleteGardenerResourceManagers := g.Add(flow.Task{
		Name: "Deleting stale gardener-resource-managers",
		Fn: func(ctx context.Context) error {
			return b.DeleteGardenerResourceManagers(ctx, (*clientSet).Client())
		},
		Dependencies: flow.NewTaskIDs(finalizeGNAManagedResource),
	})
	deleteExtensions := g.Add(flow.Task{
		Name: "Deleting stale extensions",
		Fn: func(ctx context.Context) error {
			return b.DeleteExtensions(ctx, (*clientSet).Client())
		},
		Dependencies: flow.NewTaskIDs(finalizeGNAManagedResource),
	})
	_ = g.Add(flow.Task{
		Name: "Deleting prior control plane Node",
		Fn: func(ctx context.Context) error {
			return b.DeletePriorNode(ctx, (*clientSet).Client(), priorNodeName)
		},
	})
	_ = g.Add(flow.Task{
		Name: "Force deleting Pods running on the prior control plane Node",
		Fn: func(ctx context.Context) error {
			return b.ForceDeletePriorNodePods(ctx, (*clientSet).Client(), priorNodeName)
		},
		Dependencies: flow.NewTaskIDs(deleteGardenerResourceManagers, deleteExtensions),
	})
	_ = g.Add(flow.Task{
		Name: "Deleting stale etcd member Leases",
		Fn: func(ctx context.Context) error {
			return b.DeleteStaleEtcdMemberLeases(ctx, (*clientSet).Client())
		},
	})
	_ = g.Add(flow.Task{
		Name: "Deleting stale gardener-node-agent CertificateSigningRequests",
		Fn: func(ctx context.Context) error {
			return b.DeleteNodeAgentCertificateSigningRequests(ctx, (*clientSet).Client())
		},
		Dependencies: flow.NewTaskIDs(deleteGardenerResourceManagers),
	})

	return g
}

// TaskGroupActivateGardenerNodeAgent is a flow.TaskID for a logical flow.TaskGroup.
const TaskGroupActivateGardenerNodeAgent flow.TaskID = "TaskGroupActivateGardenerNodeAgent"

// ActivateGardenerNodeAgentTaskGroup returns the flow.TaskGroup that activates gardener-node-agent, approves its client
// certificate signing request and waits for the control plane Node to be labeled. The waiting task is skipped when the
// pod network is already available. This chain is shared by the `gardenadm init` and `gardenadm restore` flows.
func (b *GardenadmBotanist) ActivateGardenerNodeAgentTaskGroup(podNetworkAvailable bool) flow.TaskGroup {
	g := flow.NewTaskGroup(TaskGroupActivateGardenerNodeAgent).WithDependencies(botanist.TaskGroupInitializeSecretsManagement)

	activateGardenerNodeAgent := g.Add(flow.Task{
		Name: "Activating gardener-node-agent",
		Fn:   b.ActivateGardenerNodeAgent,
	})
	approveGardenerNodeAgentCSR := g.Add(flow.Task{
		Name:         "Approving gardener-node-agent client certificate signing request",
		Fn:           flow.TaskFn(b.ApproveNodeAgentCertificateSigningRequest).RetryUntilTimeout(2*time.Second, time.Minute),
		Dependencies: flow.NewTaskIDs(activateGardenerNodeAgent),
	})
	_ = g.Add(flow.Task{
		Name:         "Waiting until the control plane Node is labeled",
		Fn:           flow.TaskFn(b.CheckControlPlaneNodeLabeled).RetryUntilTimeout(2*time.Second, 2*time.Minute),
		SkipIf:       podNetworkAvailable,
		Dependencies: flow.NewTaskIDs(approveGardenerNodeAgentCSR),
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
