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
	"github.com/gardener/gardener/pkg/client/kubernetes"
	gardenadmbotanist "github.com/gardener/gardener/pkg/gardenadm/botanist"
	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	"github.com/gardener/gardener/pkg/gardenlet/operation/botanist"
	"github.com/gardener/gardener/pkg/utils/flow"
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
	phaseOpts.Bootstrap = true

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

	phaseOpts.Bootstrap = false
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
		g           = flow.NewGraph("init")
		allowBackup = v1beta1helper.GetBackupConfigForShoot(b.Shoot.GetInfo(), nil) != nil

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
		reconcileGardenerResourceManager = g.AddGroup(
			b.ReconcileGardenerResourceManagerTaskGroup(podNetworkAvailable, shootIsGarden).
				WithDependencies(approveGardenerNodeAgentCSR),
		)
		_                             = g.AddGroup(b.ReconcileSystemResourcesTaskGroup())
		reconcileExtensionControllers = g.AddGroup(b.ReconcileExtensionControllersTaskGroup(podNetworkAvailable))
		reconcileNetworkPolicies      = g.AddGroup(b.ReconcileNetworkPoliciesTaskGroup())
		_                             = g.AddGroup(
			b.ReconcileInfrastructureTaskGroup().
				WithDependencies(gardenadmbotanist.TaskGroupReconcileExtensionControllers),
		)
		_                         = g.AddGroup(b.ReconcileShootNamespacesTaskGroup())
		reconcileSystemComponents = g.AddGroup(
			b.ReconcileSystemComponentsTaskGroup().
				WithDependencies(gardenadmbotanist.TaskGroupReconcileNetworkPolicies),
		)

		reconcileGardenerResourceManagerInPodNetwork = g.AddGroup(
			b.ReconcileGardenerResourceManagerTaskGroup(true, shootIsGarden).
				WithID(botanist.TaskGroupReconcileGardenerResourceManager + "InPodNetwork").
				WithDependencies(reconcileSystemComponents).
				SkipIf(podNetworkAvailable || opts.UseHostNetwork),
		)
		reconcileExtensionControllersInPodNetwork = g.AddGroup(
			b.ReconcileExtensionControllersTaskGroup(true).
				WithID(gardenadmbotanist.TaskGroupReconcileExtensionControllers + "InPodNetwork").
				WithDependencies(reconcileGardenerResourceManagerInPodNetwork).
				SkipIf(podNetworkAvailable || opts.UseHostNetwork),
		)
		syncPointBootstrapped = flow.NewTaskIDs(
			reconcileNetworkPolicies,
			reconcileGardenerResourceManager,
			reconcileGardenerResourceManagerInPodNetwork,
			reconcileExtensionControllers,
			reconcileExtensionControllersInPodNetwork,
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
		reconcileControlPlane = g.AddGroup(
			b.ReconcileControlPlaneTaskGroup().
				WithDependencies(gardenadmbotanist.TaskGroupReconcileExtensionControllers),
		)
		deployEtcdDruid = g.Add(flow.Task{
			Name:         "Deploying ETCD Druid",
			Fn:           b.Shoot.Components.ControlPlane.EtcdDruid.Deploy,
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
			Fn:           b.WaitUntilEtcdsReady,
			SkipIf:       opts.UseBootstrapEtcd,
			Dependencies: flow.NewTaskIDs(deployEtcds),
		})
		deployControlPlaneDeployments = g.Add(flow.Task{
			Name: "Deploying control plane components as Deployments/StatefulSets and updating gardener-node-agent Secret",
			Fn: func(ctx context.Context) error {
				return b.DeployStaticControlPlaneDeployments(ctx, opts.UseBootstrapEtcd)
			},
			Dependencies: flow.NewTaskIDs(reconcileControlPlane, waitUntilEtcdsReady),
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
					b.Shoot.Components.ControlPlane.RuntimeResourceManager.Wait,
					b.Shoot.Components.ControlPlane.ResourceManager.Wait,
				),
				b.WaitUntilExtensionControllerInstallationsHealthy,
			).RetryUntilTimeout(time.Second, 5*time.Minute),
			Dependencies: flow.NewTaskIDs(waitUntilKubeControllerManagerIsActive),
		})
		_ = g.AddGroup(
			b.ReconcileMachineControllerManagerTaskGroup().
				WithDependencies(waitUntilWebhookComponentsReady),
		)
		reconcileWorker = g.AddGroup(
			b.ReconcileWorkerTaskGroup().
				WithDependencies(syncPointBootstrapped),
		)
		// We need to deploy the worker before activating the node-agent-authorizer. Without the machine objects,
		// the node-agent-authorizer would reject requests from gardener-node-agent because it cannot find a corresponding
		// machine for them.
		finalizeGardenerNodeAgentBootstrapping = g.Add(flow.Task{
			Name:         "Finalizing gardener-node-agent bootstrapping (remove cluster-admin access, activate node-agent authorizer)",
			Fn:           b.FinalizeGardenerNodeAgentBootstrapping,
			Dependencies: flow.NewTaskIDs(reconcileWorker),
		})
		waitUntilGardenerNodeAgentLeaseIsRenewed = g.Add(flow.Task{
			Name:         "Waiting until gardener-node-agent lease is renewed",
			Fn:           b.WaitUntilGardenerNodeAgentLeaseIsRenewed,
			Dependencies: flow.NewTaskIDs(finalizeGardenerNodeAgentBootstrapping),
		})
		waitUntilWorkerStatusUpdate = g.Add(flow.Task{
			Name: "Waiting until worker resource status is updated with latest machine deployments",
			Fn: func(ctx context.Context) error {
				return b.Shoot.Components.Extensions.Worker.WaitUntilWorkerStatusMachineDeploymentsUpdated(ctx)
			},
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(waitUntilGardenerNodeAgentLeaseIsRenewed),
		})
		_ = g.Add(flow.Task{
			Name:         "Deploying cluster-autoscaler",
			Fn:           b.DeployClusterAutoscaler,
			SkipIf:       !b.Shoot.HasManagedInfrastructure(),
			Dependencies: flow.NewTaskIDs(waitUntilWorkerStatusUpdate),
		})
	)

	if err := g.Compile().Run(ctx, flow.Opts{
		Log:              opts.Log,
		ProgressReporter: flow.NewCommandLineProgressReporter(opts.ErrOut),
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

func bootstrapControlPlane(ctx context.Context, opts *Options) (*gardenadmbotanist.GardenadmBotanist, error) {
	b, err := gardenadmbotanist.NewGardenadmBotanistFromManifests(ctx, opts.Log, nil, opts.ConfigDir, true)
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

		if opts.Force {
			b.Logger.Info("Force flag is set, skipping check for existing gardenlet deployment in shoot control plane namespace")
		} else {
			clientSet, err := b.CreateClientSet(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed creating client set from existing kubeconfig file %s: %w", botanist.PathKubeconfig, err)
			}

			if gardenletExists, err := cmd.IsGardenletDeployed(ctx, clientSet.Client(), b.Shoot.ControlPlaneNamespace); err != nil {
				return nil, fmt.Errorf("failed checking if gardenlet is already deployed: %w", err)
			} else if gardenletExists {
				return nil, fmt.Errorf("found existing gardenlet deployment in shoot control plane namespace %s, aborting initialization", b.Shoot.ControlPlaneNamespace)
			}
		}
	}

	var (
		clientSet kubernetes.Interface
		g         = flow.NewGraph("bootstrap")
		reporter  = flow.NewCommandLineProgressReporter(opts.ErrOut)

		initializeSecretsManagement = g.Add(flow.Task{
			Name:   "Initializing secrets management",
			Fn:     b.InitializeSecretsManagement,
			SkipIf: kubeconfigFileExists && !b.Shoot.IsRestorePhase(),
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
			SkipIf:       b.Shoot.IsRestorePhase(),
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
		importSecrets = g.Add(flow.Task{
			Name: "Importing secrets into control plane",
			Fn: func(ctx context.Context) error {
				return b.MigrateSecrets(ctx, b.SeedClientSet.Client(), clientSet.Client())
			},
			SkipIf:       kubeconfigFileExists && !b.Shoot.IsRestorePhase(),
			Dependencies: flow.NewTaskIDs(persistBootstrapSecrets, initializeClientSet),
		})
		_ = g.Add(flow.Task{
			Name: "Deleting temporary ShootState containing bootstrap secrets",
			Fn: func(_ context.Context) error {
				return b.CleanupBootstrapSecrets(opts.ConfigDir)
			},
			Dependencies: flow.NewTaskIDs(importSecrets),
		})
	)

	if err := g.Compile().Run(ctx, flow.Opts{
		Log:              b.Logger,
		ProgressReporter: reporter,
	}); err != nil {
		return nil, flow.Errors(err)
	}

	return gardenadmbotanist.NewGardenadmBotanistFromManifests(ctx, opts.Log, clientSet, opts.ConfigDir, true)
}
