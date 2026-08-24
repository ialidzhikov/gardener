// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package botanist

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/component-base/version"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/gardener/gardener/imagevector"
	v1beta1helper "github.com/gardener/gardener/pkg/api/core/v1beta1/helper"
	nodeagentconfigv1alpha1 "github.com/gardener/gardener/pkg/apis/config/nodeagent/v1alpha1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/component/extensions/operatingsystemconfig"
	"github.com/gardener/gardener/pkg/component/extensions/operatingsystemconfig/nodeinit"
	nodeagentcomponent "github.com/gardener/gardener/pkg/component/extensions/operatingsystemconfig/original/components/nodeagent"
	"github.com/gardener/gardener/pkg/nodeagent"
	nodeagentcontainerd "github.com/gardener/gardener/pkg/nodeagent/containerd"
	operatingsystemconfigcontroller "github.com/gardener/gardener/pkg/nodeagent/controller/operatingsystemconfig"
	"github.com/gardener/gardener/pkg/nodeagent/registry"
)

// DeployOperatingSystemConfigSecretForBootstrap deploys the OperatingSystemConfig resource and adds its content into
// a Secret so that gardener-node-agent can read it and reconcile its content.
func (b *GardenadmBotanist) DeployOperatingSystemConfigSecretForBootstrap(ctx context.Context) error {
	if err := b.DeployControlPlaneDeployments(ctx, true); err != nil {
		return fmt.Errorf("failed deploying control plane deployments: %w", err)
	}

	oscData, controlPlaneWorkerPoolName, err := b.DeployOperatingSystemConfigWithStaticPods(ctx, true)
	if err != nil {
		return fmt.Errorf("failed deploying OperatingSystemConfig: %w", err)
	}

	return b.createOperatingSystemConfigSecretForNodeAgent(ctx, oscData.Object, oscData.GardenerNodeAgentSecretName, controlPlaneWorkerPoolName)
}

// OperatingSystemConfigSecret returns the gardener-node-agent OperatingSystemConfig secret that was most recently built
// by this botanist (e.g., during the bootstrap phase). It may be nil if no such secret has been created yet.
func (b *GardenadmBotanist) OperatingSystemConfigSecret() *corev1.Secret {
	return b.operatingSystemConfigSecret
}

// SetOperatingSystemConfigSecret sets the gardener-node-agent OperatingSystemConfig secret on this botanist. It is used
// to carry the secret built during the bootstrap phase over to the botanist connected to the real API server.
func (b *GardenadmBotanist) SetOperatingSystemConfigSecret(secret *corev1.Secret) {
	b.operatingSystemConfigSecret = secret
}

// OverwriteOperatingSystemConfigSecret overwrites the gardener-node-agent OperatingSystemConfig secret in the cluster
// with the content of the given secret (matching it by name).
//
// This is needed on restore: the OperatingSystemConfig secret name is content-independent (it is derived from cluster
// parameters, not from the list of static-pod manifests), so the secret restored from the etcd snapshot carries the
// name of the current node-agent secret but the *managed* etcd static-pod manifests (etcd-main.yaml/etcd-events.yaml).
// If the systemd gardener-node-agent were activated against that restored secret, its very first reconciliation would
// already materialize the managed etcd static pods during the bootstrap phase - long before the etcd-druid transition
// happens - which conflicts with the bootstrap etcd and breaks the control plane. By overwriting the secret with the
// bootstrap content (built during the bootstrap phase) before activation, the node-agent's first reconciliation
// deploys the bootstrap etcd instead, mirroring the healthy lineage of `gardenadm init` and the first
// `gardenadm restore`. The later etcd-druid transition rewrites the secret with the managed content, at which point the
// node-agent removes the bootstrap etcd manifests as usual.
func (b *GardenadmBotanist) OverwriteOperatingSystemConfigSecret(ctx context.Context, bootstrapSecret *corev1.Secret) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bootstrapSecret.Name, Namespace: bootstrapSecret.Namespace}}

	if _, err := controllerutil.CreateOrUpdate(ctx, b.SeedClientSet.Client(), secret, func() error {
		secret.Labels = bootstrapSecret.Labels
		secret.Annotations = bootstrapSecret.Annotations
		secret.Type = bootstrapSecret.Type
		secret.Data = bootstrapSecret.Data
		return nil
	}); err != nil {
		return fmt.Errorf("failed overwriting the OperatingSystemConfig secret %q for gardener-node-agent with bootstrap content: %w", client.ObjectKeyFromObject(secret), err)
	}

	return nil
}

func (b *GardenadmBotanist) createOperatingSystemConfigSecretForNodeAgent(ctx context.Context, osc *extensionsv1alpha1.OperatingSystemConfig, secretName, poolName string) error {
	var err error

	b.operatingSystemConfigSecret, err = nodeagentcomponent.OperatingSystemConfigSecret(ctx, b.SeedClientSet.Client(), osc, secretName, poolName, false)
	if err != nil {
		return fmt.Errorf("failed computing the OperatingSystemConfig secret for gardener-node-agent for pool %q: %w", poolName, err)
	}

	return b.SeedClientSet.Client().Create(ctx, b.operatingSystemConfigSecret)
}

// ApplyOperatingSystemConfig runs gardener-node-agent's reconciliation logic in order to apply the
// OperatingSystemConfig.
func (b *GardenadmBotanist) ApplyOperatingSystemConfig(ctx context.Context) error {
	if b.operatingSystemConfigSecret == nil {
		return fmt.Errorf("operating system config secret is nil, make sure to call createOperatingSystemConfigSecretForNodeAgent() first")
	}

	if err := b.ensureGardenerNodeAgentDirectories(); err != nil {
		return fmt.Errorf("failed ensuring gardener-node-agent directories exist: %w", err)
	}

	// Write zone file if zone is configured.
	if b.Zone != nil {
		if err := b.FS.WriteFile(nodeagentconfigv1alpha1.ZoneFilePath, []byte(*b.Zone), 0600); err != nil {
			return fmt.Errorf("failed writing zone file: %w", err)
		}
	}

	node, err := nodeagent.FetchNodeByHostName(ctx, b.SeedClientSet.Client(), b.HostName)
	if err != nil {
		return fmt.Errorf("failed fetching node object by hostname %q: %w", b.HostName, err)
	}

	containerdclient, err := nodeagentcontainerd.NewClient()
	if err != nil {
		return fmt.Errorf("failed connecting to containerd: %w", err)
	}

	reconcilerCtx, cancelFunc := context.WithCancel(ctx) // #nosec: G118 -- cancelFunc is passed to Reconciler.CancelContext.
	reconcilerCtx = log.IntoContext(reconcilerCtx, b.Logger.WithName("operatingsystemconfig-reconciler").WithValues("secret", client.ObjectKeyFromObject(b.operatingSystemConfigSecret)))

	_, err = (&operatingsystemconfigcontroller.Reconciler{
		APIReader: b.SeedClientSet.APIReader(),
		Client:    b.SeedClientSet.Client(),
		Config: nodeagentconfigv1alpha1.OperatingSystemConfigControllerConfig{
			SyncPeriod:        &metav1.Duration{Duration: time.Minute},
			SecretName:        b.operatingSystemConfigSecret.Name,
			KubernetesVersion: b.Shoot.KubernetesVersion,
		},
		ConfigDir:             nodeagentconfigv1alpha1.BaseDir,
		CancelContext:         cancelFunc,
		Recorder:              &events.FakeRecorder{},
		Extractor:             registry.NewExtractor(),
		HostName:              b.HostName,
		NodeName:              ptr.Deref(node, corev1.Node{}).Name,
		DBus:                  b.DBus,
		FS:                    b.FS,
		SkipWritingStateFiles: true,
		ContainerdClient:      containerdclient,
	}).Reconcile(reconcilerCtx, reconcile.Request{NamespacedName: types.NamespacedName{Name: b.operatingSystemConfigSecret.Name, Namespace: b.operatingSystemConfigSecret.Namespace}})
	return err
}

func (b *GardenadmBotanist) ensureGardenerNodeAgentDirectories() error {
	if err := b.FS.MkdirAll(nodeagentconfigv1alpha1.TempDir, os.ModeDir); err != nil {
		return fmt.Errorf("failed creating temporary directory (%q): %w", nodeagentconfigv1alpha1.TempDir, err)
	}
	if err := b.FS.MkdirAll(nodeagentconfigv1alpha1.CredentialsDir, os.ModeDir); err != nil {
		return fmt.Errorf("failed creating credentials directory (%q): %w", nodeagentconfigv1alpha1.CredentialsDir, err)
	}
	return nil
}

// PrepareGardenerNodeInitConfiguration creates a Secret containing an OperatingSystemConfig with the gardener-node-init
// unit.
func (b *GardenadmBotanist) PrepareGardenerNodeInitConfiguration(ctx context.Context, secretName, controlPlaneAddress string, caBundle []byte, bootstrapToken string) error {
	osc, err := b.generateGardenerNodeInitOperatingSystemConfig(secretName, controlPlaneAddress, bootstrapToken, caBundle)
	if err != nil {
		return fmt.Errorf("failed computing units and files for gardener-node-init: %w", err)
	}

	return b.createOperatingSystemConfigSecretForNodeAgent(ctx, osc, secretName, "")
}

func (b *GardenadmBotanist) generateGardenerNodeInitOperatingSystemConfig(secretName, controlPlaneAddress, bootstrapToken string, caBundle []byte) (*extensionsv1alpha1.OperatingSystemConfig, error) {
	image, err := imagevector.Containers().FindImage(imagevector.ContainerImageNameGardenerNodeAgent)
	if err != nil {
		return nil, fmt.Errorf("failed finding image %q: %w", imagevector.ContainerImageNameGardenerNodeAgent, err)
	}
	image.WithOptionalTag(version.Get().GitVersion)

	units, files, err := nodeinit.Config(
		gardencorev1beta1.Worker{},
		image.String(),
		nodeagentcomponent.ComponentConfig(secretName, b.Shoot.KubernetesVersion, controlPlaneAddress, nil),
		caBundle,
	)
	if err != nil {
		return nil, fmt.Errorf("failed computing units and files for gardener-node-init: %w", err)
	}

	for i, file := range files {
		if file.Path == nodeagentconfigv1alpha1.BootstrapTokenFilePath {
			files[i].Content.Inline.Data = bootstrapToken
		}
		if file.Path == nodeagentconfigv1alpha1.MachineNameFilePath {
			files[i].Content.Inline.Data = b.HostName
		}
	}

	return &extensionsv1alpha1.OperatingSystemConfig{
		Spec: extensionsv1alpha1.OperatingSystemConfigSpec{
			Files: files,
			Units: units,
		},
	}, nil
}

// ControlPlaneBootstrapOperatingSystemConfig creates the deployer for the OperatingSystemConfig custom resource that is
// used for bootstrapping control plane nodes in `gardenadm bootstrap`.
func (b *GardenadmBotanist) ControlPlaneBootstrapOperatingSystemConfig() (operatingsystemconfig.Interface, error) {
	worker := v1beta1helper.ControlPlaneWorkerPoolForShoot(b.Shoot.GetInfo().Spec.Provider.Workers)
	if worker == nil {
		return nil, fmt.Errorf("did not find the control plane worker pool of the shoot")
	}

	values, err := b.OperatingSystemConfigValues()
	if err != nil {
		return nil, fmt.Errorf("failed creating operating system config values: %w", err)
	}

	return operatingsystemconfig.NewControlPlaneBootstrap(
		b.Logger,
		b.SeedClientSet.Client(),
		b.SecretsManager,
		&operatingsystemconfig.ControlPlaneBootstrapValues{
			Values: values,
			Worker: worker,
		},
		operatingsystemconfig.DefaultInterval,
		operatingsystemconfig.DefaultSevereThreshold,
		operatingsystemconfig.DefaultTimeout,
	), nil
}
