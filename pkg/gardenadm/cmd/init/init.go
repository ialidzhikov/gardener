// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package init

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	v1beta1helper "github.com/gardener/gardener/pkg/api/core/v1beta1/helper"
	appsv1 "k8s.io/api/apps/v1"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	seedsystem "github.com/gardener/gardener/pkg/component/seed/system"
	gardenerextensions "github.com/gardener/gardener/pkg/extensions"
	"github.com/gardener/gardener/pkg/gardenadm/botanist"
	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	"github.com/gardener/gardener/pkg/utils/flow"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
	gardenletutils "github.com/gardener/gardener/pkg/utils/gardener/gardenlet"
)

// NewCommand creates a new cobra.Command.
func NewCommand(globalOpts *cmd.Options) *cobra.Command {
	opts := &Options{Options: globalOpts}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the first control plane node",
		Long:  "Bootstrap the first control plane node",

		Example: `# Bootstrap the first control plane node
gardenadm init --config-dir /path/to/manifests

# Bootstrap the first control plane node in a specific zone (required when multiple zones are configured in the ` + "`Shoot`" + ` resource)
gardenadm init --config-dir /path/to/manifests --zone zone-a`,

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
	if opts.Recover {
		return runRecover(ctx, opts)
	}

	return runInit(ctx, opts)
}

func runRecover(ctx context.Context, opts *Options) error {
	phaseOpts := *opts
	phaseOpts.Recover = false

	if _, err := bootstrapControlPlane(ctx, &phaseOpts); err != nil {
		return fmt.Errorf("failed first recovery phase: %w", err)
	}

	b, err := botanist.NewGardenadmBotanistFromManifests(ctx, opts.Log, nil, opts.ConfigDir, true)
	if err != nil {
		return fmt.Errorf("failed preparing recover cleanup: %w", err)
	}
	b.Logger.Info("TESTOOOOOOOOOO")
	clientSet, err := b.CreateClientSet(ctx)
	if err != nil {
		return fmt.Errorf("failed creating client for recover cleanup: %w", err)
	}
	b.SeedClientSet = clientSet
	b.ShootClientSet = clientSet

	if err := prepareRecoverSecondPhase(ctx, b); err != nil {
		return fmt.Errorf("failed preparing second recovery phase: %w", err)
	}

	phaseOpts.UseBootstrapEtcd = true

	return runInit(ctx, &phaseOpts)
}

func prepareRecoverSecondPhase(ctx context.Context, b *botanist.GardenadmBotanist) error {
	b.Logger.Info("Preparing second recovery phase cleanup")

	csrList := &certificatesv1.CertificateSigningRequestList{}
	if err := b.SeedClientSet.Client().List(ctx, csrList); err != nil {
		return fmt.Errorf("failed listing certificate signing requests: %w", err)
	}
	for _, csr := range csrList.Items {
		if csr.Spec.SignerName != "kubernetes.io/kube-apiserver-client" {
			continue
		}
		b.Logger.Info("Deleting CSR", "name", csr.Name)
		if err := b.SeedClientSet.Client().Delete(ctx, &csr); crclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting certificate signing request %q: %w", csr.Name, err)
		}
	}

	managedResourceList := &resourcesv1alpha1.ManagedResourceList{}
	if err := b.SeedClientSet.Client().List(ctx, managedResourceList); err != nil {
		return fmt.Errorf("failed listing managedresources: %w", err)
	}
	for _, mr := range managedResourceList.Items {
		obj := mr.DeepCopy()
		obj.Finalizers = deleteFinalizer(obj.Finalizers, "resources.gardener.cloud/gardener-resource-manager")
		b.Logger.Info("Updating ManagedResource before deletion", "namespace", obj.Namespace, "name", obj.Name)
		if err := b.SeedClientSet.Client().Update(ctx, obj); crclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed updating managedresource %s/%s: %w", obj.Namespace, obj.Name, err)
		}
		b.Logger.Info("Deleting ManagedResource", "namespace", obj.Namespace, "name", obj.Name)
		if err := b.SeedClientSet.Client().Delete(ctx, obj); crclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting managedresource %s/%s: %w", obj.Namespace, obj.Name, err)
		}
	}

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: b.HostName}}
	b.Logger.Info("Deleting node", "name", b.HostName)
	if err := b.SeedClientSet.Client().Delete(ctx, node); crclient.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed deleting node %q: %w", b.HostName, err)
	}

	podList := &corev1.PodList{}
	if err := b.SeedClientSet.Client().List(ctx, podList); err != nil {
		return fmt.Errorf("failed listing pods: %w", err)
	}
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != b.HostName {
			continue
		}
		b.Logger.Info("Force deleting pod on recovery node", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
		deletePolicy := metav1.DeletePropagationBackground
		if err := b.SeedClientSet.Client().Delete(ctx, pod.DeepCopy(), &crclient.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0), PropagationPolicy: &deletePolicy}); crclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed force deleting pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}

	grmDeployments := &appsv1.DeploymentList{}
	if err := b.SeedClientSet.Client().List(ctx, grmDeployments, crclient.MatchingLabels{v1beta1constants.LabelApp: "gardener-resource-manager"}); err != nil {
		return fmt.Errorf("failed listing gardener-resource-manager deployments: %w", err)
	}
	for _, deployment := range grmDeployments.Items {
		b.Logger.Info("Deleting gardener-resource-manager deployment", "namespace", deployment.Namespace, "name", deployment.Name)
		if err := b.SeedClientSet.Client().Delete(ctx, deployment.DeepCopy()); crclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
		}
	}

	mcmDeploymentList := &appsv1.DeploymentList{}
	if err := b.SeedClientSet.Client().List(ctx, mcmDeploymentList); err != nil {
		return fmt.Errorf("failed listing deployments for machine-controller-manager cleanup: %w", err)
	}
	for _, deployment := range mcmDeploymentList.Items {
		if deployment.Name != "machine-controller-manager" {
			continue
		}
		b.Logger.Info("Deleting machine-controller-manager deployment", "namespace", deployment.Namespace, "name", deployment.Name)
		if err := b.SeedClientSet.Client().Delete(ctx, deployment.DeepCopy()); crclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
		}
	}

	b.Logger.Info("Finished second recovery phase cleanup")

	return nil
}

func deleteFinalizer(finalizers []string, finalizer string) []string {
	result := make([]string, 0, len(finalizers))
	for _, f := range finalizers {
		if f == finalizer {
			continue
		}
		result = append(result, f)
	}
	return result
}

func runInit(ctx context.Context, opts *Options) error {
	b, err := bootstrapControlPlane(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed bootstrapping control plane: %w", err)
	}

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
		g                = flow.NewGraph("init")
		reporter         = flow.NewCommandLineProgressReporter(opts.ErrOut)
		allowBackup      = v1beta1helper.GetBackupConfigForShoot(b.Shoot.GetInfo(), nil) != nil
		kubeProxyEnabled = v1beta1helper.KubeProxyEnabled(b.Shoot.GetInfo().Spec.Kubernetes.KubeProxy)

		deployControlPlaneNamespace = g.Add(flow.Task{
			Name: "Deploying control plane namespace",
			Fn:   b.DeployControlPlaneNamespace,
		})
		deployGardenNamespace = g.Add(flow.Task{
			Name: "Deploying garden namespace",
			Fn: func(ctx context.Context) error {
				return gardenerutils.ReconcileGardenNamespace(ctx, b.SeedClientSet.Client(), v1beta1constants.GardenNamespace, b.Seed.GetInfo().Spec.Provider.Zones, true, nil)
			},
		})
		deployCloudProviderSecret = g.Add(flow.Task{
			Name:         "Deploying cloud provider account secret",
			Fn:           b.DeployCloudProviderSecret,
			SkipIf:       b.Shoot.Credentials == nil,
			Dependencies: flow.NewTaskIDs(deployControlPlaneNamespace),
		})
		reconcileCustomResourceDefinitions = g.Add(flow.Task{
			Name: "Reconciling CustomResourceDefinitions",
			Fn:   b.ReconcileCustomResourceDefinitions,
		})
		ensureCustomResourceDefinitionsReady = g.Add(flow.Task{
			Name:         "Ensuring CustomResourceDefinitions are ready",
			Fn:           flow.TaskFn(b.EnsureCustomResourceDefinitionsReady).RetryUntilTimeout(time.Second, time.Minute),
			Dependencies: flow.NewTaskIDs(reconcileCustomResourceDefinitions),
		})
		reconcileClusterResource = g.Add(flow.Task{
			Name: "Reconciling extensions.gardener.cloud/v1alpha1.Cluster resource",
			Fn: func(ctx context.Context) error {
				return gardenerextensions.SyncClusterResourceToSeed(ctx, b.SeedClientSet.Client(), b.Shoot.ControlPlaneNamespace, b.Shoot.GetInfo(), b.Shoot.CloudProfile, b.Seed.GetInfo())
			},
			Dependencies: flow.NewTaskIDs(ensureCustomResourceDefinitionsReady),
		})
		initializeSecretsManagement = g.Add(flow.Task{
			Name:         "Initializing internal state of Gardener secrets manager",
			Fn:           b.InitializeSecretsManagement,
			Dependencies: flow.NewTaskIDs(reconcileClusterResource),
		})
		activateGardenerNodeAgent = g.Add(flow.Task{
			Name:         "Activating gardener-node-agent",
			Fn:           b.ActivateGardenerNodeAgent,
			Dependencies: flow.NewTaskIDs(initializeSecretsManagement),
		})
		approveGardenerNodeAgentCSR = g.Add(flow.Task{
			Name:         "Approving gardener-node-agent client certificate signing request",
			Fn:           flow.TaskFn(b.ApproveNodeAgentCertificateSigningRequest).RetryUntilTimeout(2*time.Second, time.Minute),
			Dependencies: flow.NewTaskIDs(activateGardenerNodeAgent),
		})
		verifyControlPlaneNodeExists = g.Add(flow.Task{
			Name: "Verifying control-plane worker node exists",
			Fn: flow.TaskFn(func(ctx context.Context) error {
				nodes := &corev1.NodeList{}
				if err := b.SeedClientSet.Client().List(ctx, nodes, crclient.MatchingLabels(map[string]string{"worker.gardener.cloud/pool": "control-plane"})); err != nil {
					return fmt.Errorf("listing nodes with label worker.gardener.cloud/pool=control-plane: %w", err)
				}
				if len(nodes.Items) == 0 {
					return fmt.Errorf("no node with label worker.gardener.cloud/pool=control-plane found; ensure a control-plane node has joined")
				}
				return nil
			}).RetryUntilTimeout(5*time.Second, 2*time.Minute),
			Dependencies: flow.NewTaskIDs(approveGardenerNodeAgentCSR, deployGardenNamespace),
		})
		deployGardenerResourceManager = g.Add(flow.Task{
			Name: "Deploying gardener-resource-manager",
			Fn: func(ctx context.Context) error {
				b.Components.RuntimeResourceManager.SetBootstrapControlPlaneNode(!podNetworkAvailable)
				b.Shoot.Components.ControlPlane.ResourceManager.SetBootstrapControlPlaneNode(!podNetworkAvailable)

				if shootIsGarden {
					return b.Shoot.Components.ControlPlane.ResourceManager.Deploy(ctx)
				}

				return flow.Parallel(
					b.Components.RuntimeResourceManager.Deploy,
					b.Shoot.Components.ControlPlane.ResourceManager.Deploy,
				)(ctx)
			},
			Dependencies: flow.NewTaskIDs(approveGardenerNodeAgentCSR, deployGardenNamespace, verifyControlPlaneNodeExists),
		})
		waitUntilGardenerResourceManagerReady = g.Add(flow.Task{
			Name: "Waiting until gardener-resource-manager reports readiness",
			Fn: func(ctx context.Context) error {
				if shootIsGarden {
					return b.Shoot.Components.ControlPlane.ResourceManager.Wait(ctx)
				}

				return flow.Parallel(
					b.Components.RuntimeResourceManager.Wait,
					b.Shoot.Components.ControlPlane.ResourceManager.Wait,
				)(ctx)
			},
			Dependencies: flow.NewTaskIDs(deployGardenerResourceManager),
		})
		_ = g.Add(flow.Task{
			Name: "Deploying seed system resources",
			Fn: func(ctx context.Context) error {
				return seedsystem.New(b.SeedClientSet.Client(), b.Shoot.ControlPlaneNamespace, seedsystem.Values{ManagePriorityClasses: true}).Deploy(ctx)
			},
			Dependencies: flow.NewTaskIDs(waitUntilGardenerResourceManagerReady),
		})
		_ = g.Add(flow.Task{
			Name:         "Deploying shoot system resources",
			Fn:           b.DeployShootSystem,
			Dependencies: flow.NewTaskIDs(waitUntilGardenerResourceManagerReady),
		})
		deployExtensionControllers = g.Add(flow.Task{
			Name: "Deploying extension controllers",
			Fn: func(ctx context.Context) error {
				return b.ReconcileExtensionControllerInstallations(ctx, !podNetworkAvailable)
			},
			Dependencies: flow.NewTaskIDs(waitUntilGardenerResourceManagerReady),
		})
		waitUntilExtensionControllersReady = g.Add(flow.Task{
			Name:         "Waiting until extension controllers report readiness",
			Fn:           b.WaitUntilExtensionControllerInstallationsHealthy,
			Dependencies: flow.NewTaskIDs(deployExtensionControllers),
		})
		deployNetworkPolicies = g.Add(flow.Task{
			Name:         "Deploying network policies",
			Fn:           b.ApplyNetworkPolicies,
			Dependencies: flow.NewTaskIDs(waitUntilGardenerResourceManagerReady, deployExtensionControllers),
		})
		deployInfrastructure = g.Add(flow.Task{
			Name:         "Deploying Shoot infrastructure",
			Fn:           b.DeployInfrastructure,
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(initializeSecretsManagement, deployCloudProviderSecret, waitUntilExtensionControllersReady),
		})
		waitUntilInfrastructureReady = g.Add(flow.Task{
			Name:         "Waiting until Shoot infrastructure has been reconciled",
			Fn:           b.WaitForInfrastructure,
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(deployInfrastructure),
		})
		deployShootNamespaces = g.Add(flow.Task{
			Name:         "Deploying shoot namespaces system component",
			Fn:           b.Shoot.Components.SystemComponents.Namespaces.Deploy,
			Dependencies: flow.NewTaskIDs(waitUntilGardenerResourceManagerReady),
		})
		waitUntilShootNamespacesReady = g.Add(flow.Task{
			Name:         "Waiting until shoot namespaces have been reconciled",
			Fn:           b.Shoot.Components.SystemComponents.Namespaces.Wait,
			Dependencies: flow.NewTaskIDs(deployShootNamespaces),
		})
		_ = g.Add(flow.Task{
			Name:         "Deploying kube-proxy system component",
			Fn:           b.DeployKubeProxy,
			SkipIf:       !kubeProxyEnabled,
			Dependencies: flow.NewTaskIDs(waitUntilShootNamespacesReady, waitUntilInfrastructureReady),
		})
		deployNetwork = g.Add(flow.Task{
			Name:         "Deploying shoot network plugin",
			Fn:           b.DeployNetwork,
			Dependencies: flow.NewTaskIDs(waitUntilShootNamespacesReady, waitUntilInfrastructureReady),
		})
		waitUntilNetworkReady = g.Add(flow.Task{
			Name:         "Waiting until shoot network plugin has been reconciled",
			Fn:           b.Shoot.Components.Extensions.Network.Wait,
			Dependencies: flow.NewTaskIDs(deployNetwork),
		})
		deployCoreDNS = g.Add(flow.Task{
			Name:         "Deploying CoreDNS system component",
			Fn:           b.DeployCoreDNS,
			Dependencies: flow.NewTaskIDs(waitUntilNetworkReady, deployNetworkPolicies),
		})
		waitUntilCoreDNSReady = g.Add(flow.Task{
			Name:         "Waiting until CoreDNS system component is ready",
			Fn:           b.Shoot.Components.SystemComponents.CoreDNS.Wait,
			Dependencies: flow.NewTaskIDs(deployCoreDNS),
		})

		deployGardenerResourceManagerIntoPodNetwork = g.Add(flow.Task{
			Name: "Redeploying gardener-resource-manager into pod network",
			Fn: func(ctx context.Context) error {
				b.Components.RuntimeResourceManager.SetBootstrapControlPlaneNode(false)
				b.Shoot.Components.ControlPlane.ResourceManager.SetBootstrapControlPlaneNode(false)

				if shootIsGarden {
					return b.Shoot.Components.ControlPlane.ResourceManager.Deploy(ctx)
				}

				return flow.Parallel(
					b.Components.RuntimeResourceManager.Deploy,
					b.Shoot.Components.ControlPlane.ResourceManager.Deploy,
				)(ctx)
			},
			SkipIf:       podNetworkAvailable || opts.UseHostNetwork,
			Dependencies: flow.NewTaskIDs(waitUntilCoreDNSReady),
		})
		waitUntilGardenerResourceManagerInPodNetworkReady = g.Add(flow.Task{
			Name: "Waiting until gardener-resource-manager (in pod network) reports readiness",
			Fn: func(ctx context.Context) error {
				if shootIsGarden {
					return b.Shoot.Components.ControlPlane.ResourceManager.Wait(ctx)
				}

				return flow.Parallel(
					b.Components.RuntimeResourceManager.Wait,
					b.Shoot.Components.ControlPlane.ResourceManager.Wait,
				)(ctx)
			},
			SkipIf:       podNetworkAvailable || opts.UseHostNetwork,
			Dependencies: flow.NewTaskIDs(deployGardenerResourceManagerIntoPodNetwork),
		})
		deployExtensionControllersIntoPodNetwork = g.Add(flow.Task{
			Name: "Redeploying extension controllers into pod network",
			Fn: flow.TaskFn(func(ctx context.Context) error {
				return b.ReconcileExtensionControllerInstallations(ctx, false)
			}).RetryUntilTimeout(5*time.Second, 30*time.Second),
			SkipIf:       podNetworkAvailable || opts.UseHostNetwork,
			Dependencies: flow.NewTaskIDs(waitUntilGardenerResourceManagerInPodNetworkReady),
		})
		waitUntilExtensionControllersInPodNetworkReady = g.Add(flow.Task{
			Name:         "Waiting until extension controllers (in pod network) report readiness",
			Fn:           b.WaitUntilExtensionControllerInstallationsHealthy,
			SkipIf:       podNetworkAvailable || opts.UseHostNetwork,
			Dependencies: flow.NewTaskIDs(deployExtensionControllersIntoPodNetwork),
		})
		syncPointBootstrapped = flow.NewTaskIDs(
			deployNetworkPolicies,
			waitUntilGardenerResourceManagerReady,
			waitUntilGardenerResourceManagerInPodNetworkReady,
			waitUntilExtensionControllersReady,
			waitUntilExtensionControllersInPodNetworkReady,
		)

		// When extension-based exposure is configured, first deploy the SelfHostedShootExposure object
		// so the extension controller provisions the necessary resources. The DNSRecord step then reads the
		// resulting ingress from the status. For DNS-based exposure the SelfHostedShootExposure
		// step is skipped and the DNSRecord step points directly at the control-plane node addresses.
		deploySelfHostedShootExposure = g.Add(flow.Task{
			Name:         "Deploying SelfHostedShootExposure",
			Fn:           b.DeploySelfHostedShootExposure,
			SkipIf:       !b.Shoot.HasExtensionExposure(),
			Dependencies: flow.NewTaskIDs(syncPointBootstrapped),
		})
		_ = g.Add(flow.Task{
			Name: "Restoring external DNSRecord",
			// Retry to tolerate the warmup window of the in-cluster extension controllers that were just redeployed
			// into the pod network: their pods are Ready, but leader-election and informer cache sync can take
			// long enough that the first Restore+Wait hits the severe-error threshold before the controller observes
			// the new generation.
			Fn:           flow.TaskFn(b.RestoreExternalDNSRecord).RetryUntilTimeout(5*time.Second, 5*time.Minute),
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(syncPointBootstrapped, deploySelfHostedShootExposure),
		})
		reconcileBackupBucket = g.Add(flow.Task{
			Name:         "Deploying BackupBucket for ETCD data",
			Fn:           b.ReconcileBackupBucket,
			SkipIf:       !allowBackup || opts.UseBootstrapEtcd,
			Dependencies: flow.NewTaskIDs(syncPointBootstrapped),
		})
		reconcileBackupEntry = g.Add(flow.Task{
			Name:         "Deploying BackupEntry for ETCD data",
			Fn:           b.ReconcileBackupEntry,
			SkipIf:       !allowBackup || opts.UseBootstrapEtcd,
			Dependencies: flow.NewTaskIDs(reconcileBackupBucket),
		})
		deployControlPlane = g.Add(flow.Task{
			Name:         "Deploying shoot control plane components",
			Fn:           b.DeployControlPlane,
			Dependencies: flow.NewTaskIDs(syncPointBootstrapped),
		})
		waitUntilControlPlaneReady = g.Add(flow.Task{
			Name:         "Waiting until shoot control plane has been reconciled",
			Fn:           b.Shoot.Components.Extensions.ControlPlane.Wait,
			Dependencies: flow.NewTaskIDs(deployControlPlane),
		})
		deployEtcdDruid = g.Add(flow.Task{
			Name:         "Deploying ETCD Druid",
			Fn:           b.DeployEtcdDruid,
			SkipIf:       opts.UseBootstrapEtcd || shootIsGarden,
			Dependencies: flow.NewTaskIDs(syncPointBootstrapped),
		})
		deployEtcds = g.Add(flow.Task{
			Name: "Deploying main and events ETCDs",
			Fn: func(ctx context.Context) error {
				machineIP, err := b.MachineIP()
				if err != nil {
					return fmt.Errorf("failed determining the machine IP address")
				}

				b.Shoot.Components.ControlPlane.EtcdMain.SetStaticPodControlPlaneNodesIPAddresses(machineIP)
				b.Shoot.Components.ControlPlane.EtcdEvents.SetStaticPodControlPlaneNodesIPAddresses(machineIP)
				return b.DeployEtcd(ctx)
			},
			SkipIf:       opts.UseBootstrapEtcd,
			Dependencies: flow.NewTaskIDs(deployEtcdDruid, reconcileBackupEntry),
		})
		waitUntilEtcdsReady = g.Add(flow.Task{
			Name:         "Waiting until main and event ETCDs have been reconciled",
			Fn:           b.WaitUntilEtcdsReconciled,
			SkipIf:       opts.UseBootstrapEtcd,
			Dependencies: flow.NewTaskIDs(deployEtcds),
		})
		deployControlPlaneDeployments = g.Add(flow.Task{
			Name:         "Deploying control plane components as Deployments/StatefulSets and updating gardener-node-agent Secret",
			Fn:           b.DeployControlPlaneDeployments,
			Dependencies: flow.NewTaskIDs(waitUntilControlPlaneReady, waitUntilEtcdsReady),
		})
		waitUntilControlPlaneDeploymentsReady = g.Add(flow.Task{
			Name: "Waiting until control plane components (static pods) are ready",
			Fn: func(ctx context.Context) error {
				return b.WaitUntilOperatingSystemConfigUpdatedForAllWorkerPools(ctx, true)
			},
			Dependencies: flow.NewTaskIDs(deployControlPlaneDeployments),
		})
		_ = g.Add(flow.Task{
			Name:         "Finalizing ETCD bootstrap transition (cleanup bootstrap ETCD left-overs)",
			Fn:           b.FinalizeEtcdBootstrapTransition,
			SkipIf:       opts.UseBootstrapEtcd,
			Dependencies: flow.NewTaskIDs(waitUntilControlPlaneDeploymentsReady),
		})
		// A lot of health checks rely on the kube-controller-manager being active. It might take some time after the
		// etcd migration for the kube-controller-manager to become active again, so we explicitly wait for that here.
		waitUntilKubeControllerManagerIsActive = g.Add(flow.Task{
			Name: "Waiting until kube-controller-manager is active",
			Fn: flow.TaskFn(func(ctx context.Context) error {
				b.Shoot.Components.ControlPlane.KubeControllerManager.SetShootClient(b.SeedClientSet.Client())
				return b.Shoot.Components.ControlPlane.KubeControllerManager.WaitForControllerToBeActive(ctx)
			}).RetryUntilTimeout(time.Second, 5*time.Minute),
			Dependencies: flow.NewTaskIDs(waitUntilControlPlaneDeploymentsReady),
		})
		// During the migration from the bootstrap etcds to the druid-managed etcds, components serving webhooks might be
		// crash-looping while retrying to connect to the API server. Therefore, we explicitly wait for them to be healthy
		// again before deploying other components.
		waitUntilWebhookComponentsReady = g.Add(flow.Task{
			Name: "Waiting until components with webhooks are ready",
			Fn: flow.Sequential(
				flow.Parallel(
					b.Components.RuntimeResourceManager.Wait,
					b.Shoot.Components.ControlPlane.ResourceManager.Wait,
				),
				b.WaitUntilExtensionControllerInstallationsHealthy,
			).RetryUntilTimeout(time.Second, 5*time.Minute),
			Dependencies: flow.NewTaskIDs(waitUntilKubeControllerManagerIsActive),
		})
		deployMachineControllerManager = g.Add(flow.Task{
			Name:         "Deploying machine-controller-manager",
			Fn:           flow.TaskFn(b.DeployMachineControllerManager).RetryUntilTimeout(time.Second, time.Minute),
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(waitUntilWebhookComponentsReady),
		})
		deployWorker = g.Add(flow.Task{
			Name:         "Deploying shoot worker pools",
			Fn:           b.DeployWorker,
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(deployMachineControllerManager),
		})
		waitUntilWorkerReady = g.Add(flow.Task{
			Name:         "Waiting until shoot worker nodes have been reconciled",
			Fn:           b.Shoot.Components.Extensions.Worker.Wait,
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(deployWorker),
		})
		// We need to deploy the worker before activating the node-agent-authorizer. Without the machine objects,
		// the node-agent-authorizer would reject requests from gardener-node-agent because it cannot find a corresponding
		// machine for them.
		finalizeGardenerNodeAgentBootstrapping = g.Add(flow.Task{
			Name:         "Finalizing gardener-node-agent bootstrapping (remove cluster-admin access, activate node-agent authorizer)",
			Fn:           b.FinalizeGardenerNodeAgentBootstrapping,
			Dependencies: flow.NewTaskIDs(waitUntilWorkerReady),
		})
		waitUntilGardenerNodeAgentLeaseIsRenewed = g.Add(flow.Task{
			Name:         "Waiting until gardener-node-agent lease is renewed",
			Fn:           b.WaitUntilGardenerNodeAgentLeaseIsRenewed,
			Dependencies: flow.NewTaskIDs(finalizeGardenerNodeAgentBootstrapping),
		})
		_ = g.Add(flow.Task{
			Name:         "Deploying cluster-autoscaler",
			Fn:           b.DeployClusterAutoscaler,
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(waitUntilGardenerNodeAgentLeaseIsRenewed),
		})
	)

	if err := g.Compile().Run(ctx, flow.Opts{
		Log:              opts.Log,
		ProgressReporter: reporter,
	}); err != nil {
		return flow.Errors(err)
	}

	fmt.Fprintf(opts.Out, `
Your Shoot cluster control-plane has initialized successfully!

To start using your cluster, you need to run the following as a regular user:

  mkdir -p $HOME/.kube
  sudo cp -i %s $HOME/.kube/config
  sudo chown $(id -u):$(id -g) $HOME/.kube/config
  kubectl get nodes

You can now join any number of control-plane or worker nodes. A bootstrap token
is required to authenticate a new machine when joining the cluster. To create
such a token, run this on a control-plane node:

  gardenadm token create --print-join-command

Copy the output and run it as root on the machine you would like to join the
cluster. Append '--control-plane' to the printed command if the machine should
be joined as a control-plane node.

Note that the above mentioned kubeconfig file will be disabled once you deploy
the gardenlet and connect this cluster to an existing Gardener installation.
Run this while targeting the garden cluster to which you want to connect this
self-hosted shoot cluster:

  gardenadm token create --print-connect-command --shoot-namespace=%s --shoot-name=%s

Copy the output and run it on a control plane node in order to deploy the
gardenlet for connectivity to Gardener.

Please use the shoots/adminkubeconfig subresource to retrieve a kubeconfig,
see https://gardener.cloud/docs/gardener/shoot/shoot_access/.
`, botanist.PathKubeconfig, b.Shoot.GetInfo().Namespace, b.Shoot.GetInfo().Name)

	return nil
}

func bootstrapControlPlane(ctx context.Context, opts *Options) (*botanist.GardenadmBotanist, error) {
	b, err := botanist.NewGardenadmBotanistFromManifests(ctx, opts.Log, nil, opts.ConfigDir, true)
	if err != nil {
		return nil, err
	}

	if opts.Zone != "" {
		b.Zone = new(opts.Zone)
	}

	kubeconfigFileExists, err := b.FS.Exists(botanist.PathKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed checking whether kubeconfig file %s exists: %w", botanist.PathKubeconfig, err)
	}

	if kubeconfigFileExists {
		b.Logger.Info("Found existing kubeconfig file, skipping initialization of control plane", "path", botanist.PathKubeconfig)
	}

	var (
		clientSet kubernetes.Interface
		g         = flow.NewGraph("bootstrap")
		reporter  = flow.NewCommandLineProgressReporter(opts.ErrOut)

		initializeSecretsManagement = g.Add(flow.Task{
			Name:   "Initializing secrets management",
			Fn:     b.InitializeSecretsManagement,
			SkipIf: kubeconfigFileExists && !b.IsRestorePhase(),
		})
		writeKubeletBootstrapKubeconfig = g.Add(flow.Task{
			Name:         "Writing kubelet bootstrap kubeconfig with a fake token to disk to make kubelet start",
			Fn:           b.WriteKubeletBootstrapKubeconfig,
			SkipIf:       kubeconfigFileExists,
			Dependencies: flow.NewTaskIDs(initializeSecretsManagement),
		})
		deployOperatingSystemConfigSecretForNodeAgent = g.Add(flow.Task{
			Name:         "Generating OperatingSystemConfig and deploying Secret for gardener-node-agent",
			Fn:           b.DeployOperatingSystemConfigSecretForBootstrap,
			SkipIf:       kubeconfigFileExists,
			Dependencies: flow.NewTaskIDs(initializeSecretsManagement),
		})
		persistBootstrapSecrets = g.Add(flow.Task{
			Name: "Persisting bootstrap secrets as ShootState for retry resilience",
			Fn: func(ctx context.Context) error {
				return b.PersistBootstrapSecrets(ctx, opts.ConfigDir)
			},
			SkipIf:       b.IsRestorePhase(),
			Dependencies: flow.NewTaskIDs(deployOperatingSystemConfigSecretForNodeAgent),
		})
		applyOperatingSystemConfig = g.Add(flow.Task{
			Name:         "Applying OperatingSystemConfig using gardener-node-agent's reconciliation logic",
			Fn:           b.ApplyOperatingSystemConfig,
			SkipIf:       kubeconfigFileExists,
			Dependencies: flow.NewTaskIDs(writeKubeletBootstrapKubeconfig, persistBootstrapSecrets),
		})
		initializeClientSet = g.Add(flow.Task{
			Name: "Initializing connection to Kubernetes control plane",
			Fn: flow.TaskFn(func(_ context.Context) error {
				clientSet, err = b.CreateClientSet(ctx)
				return err
			}).RetryUntilTimeout(2*time.Second, 2*time.Minute),
			Dependencies: flow.NewTaskIDs(applyOperatingSystemConfig),
		})
		_ = g.Add(flow.Task{
			Name: "Importing secrets into control plane",
			Fn: func(ctx context.Context) error {
				if err := b.MigrateSecrets(ctx, b.SeedClientSet.Client(), clientSet.Client()); err != nil {
					return err
				}
				return b.CleanupBootstrapSecrets(opts.ConfigDir)
			},
			SkipIf:       kubeconfigFileExists && !b.IsRestorePhase(),
			Dependencies: flow.NewTaskIDs(persistBootstrapSecrets, initializeClientSet),
		})
	)

	if err := g.Compile().Run(ctx, flow.Opts{
		Log:              b.Logger,
		ProgressReporter: reporter,
	}); err != nil {
		return nil, flow.Errors(err)
	}

	return botanist.NewGardenadmBotanistFromManifests(ctx, opts.Log, clientSet, opts.ConfigDir, true)
}
