// Copyright 2024 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package gardenercustommetrics implements the gardener-custom-metrics seed component (aka GCMx).
// For details, see the GardenerCustomMetrics type.
package gardenercustommetrics

import (
	"context"
	"fmt"
	"time"

	"github.com/Masterminds/semver/v3"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/gardener/gardener/pkg/component"
	"github.com/gardener/gardener/pkg/component/autoscaling/gardenercustommetrics/kubeobjects"
	kubernetesutils "github.com/gardener/gardener/pkg/utils/kubernetes"
	"github.com/gardener/gardener/pkg/utils/managedresources"
	secretsutils "github.com/gardener/gardener/pkg/utils/secrets"
	secretsmanager "github.com/gardener/gardener/pkg/utils/secrets/manager"
)

// ComponentName is the component name.
const ComponentName = componentBaseName

// gardenerCustomMetrics manages an instance of the gardener-custom-metrics component (aka GCMx). The component is
// deployed on a seed, scrapes the metrics from all shoot kube-apiserver pods, and provides custom metrics by
// registering as APIService at the custom metrics extension point of the seed kube-apiserver.
// For information about individual fields, see the New function.
type gardenerCustomMetrics struct {
	client         client.Client
	namespace      string
	secretsManager secretsmanager.Interface
	values         Values
}

// Values is a set of configuration values for the GardenerCustomMetrics component.
type Values struct {
	// Image is the container image.
	Image string
	// KubernetesVersion is the version of the runtime cluster.
	KubernetesVersion *semver.Version
}

// New creates a new GardenerCustomMetrics instance tied to a specific server connection.
//
// client represents the connection to the seed API server.
// namespace is where the server-side artefacts (e.g. pods) will be deployed (usually the 'garden' namespace).
// containerImageName points to the binary for the gardener-custom-metrics pods. The exact version to be used, is
// determined by contextual configuration, e.g. image vector overrides.
// secretsManager is used to interact with secrets on the seed.
func New(
	client client.Client,
	namespace string,
	secretsManager secretsmanager.Interface,
	values Values) component.DeployWaiter {
	return &gardenerCustomMetrics{
		namespace:      namespace,
		client:         client,
		secretsManager: secretsManager,
		values:         values,
	}
}

// Deploy implements [component.Deployer.Deploy]()
func (gcmx *gardenerCustomMetrics) Deploy(ctx context.Context) error {
	serverCertificateSecret, err := gcmx.deployServerCertificate(ctx)
	if err != nil {
		return fmt.Errorf("failed to delpoy the gardener-custom-metrics server TLS certificate: %w", err)
	}

	kubeObjects, err := kubeobjects.GetKubeObjectsAsYamlBytes(
		deploymentName, gcmx.namespace, gcmx.values.Image, serverCertificateSecret.Name, gcmx.values.KubernetesVersion)
	if err != nil {
		return fmt.Errorf("failed to serialize the Kubernetes objects: %w", err)
	}

	err = managedresources.CreateForSeed(ctx, gcmx.client, gcmx.namespace, managedResourceName, false, kubeObjects)
	if err != nil {
		return fmt.Errorf("failed to deploy ManagedResource '%s/%s': %w", gcmx.namespace, managedResourceName, err)
	}

	return nil
}

// Destroy implements [component.Deployer.Destroy]()
func (gcmx *gardenerCustomMetrics) Destroy(ctx context.Context) error {
	if err := managedresources.DeleteForSeed(ctx, gcmx.client, gcmx.namespace, managedResourceName); err != nil {
		return fmt.Errorf("failed to delete ManagedResource '%s/%s': %w", gcmx.namespace, managedResourceName, err)
	}

	return nil
}

// Wait implements [component.Waiter.Wait]()
func (gcmx *gardenerCustomMetrics) Wait(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, managedResourceTimeout)
	defer cancel()

	if err := managedresources.WaitUntilHealthy(timeoutCtx, gcmx.client, gcmx.namespace, managedResourceName); err != nil {
		return fmt.Errorf("failed to wait until ManagedResource '%s/%s' is healthy: %w", gcmx.namespace, managedResourceName, err)
	}

	return nil
}

// WaitCleanup implements [component.Waiter.WaitCleanup]()
func (gcmx *gardenerCustomMetrics) WaitCleanup(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, managedResourceTimeout)
	defer cancel()

	if err := managedresources.WaitUntilDeleted(timeoutCtx, gcmx.client, gcmx.namespace, managedResourceName); err != nil {
		return fmt.Errorf("failed to wait until ManagedResource '%s/%s' is deleted: %w", gcmx.namespace, managedResourceName, err)
	}

	return nil
}

const (
	componentBaseName           = "gardener-custom-metrics"
	deploymentName              = componentBaseName
	managedResourceName         = componentBaseName // The implementing artifacts are deployed to the seed via this MR
	serviceName                 = componentBaseName
	serverCertificateSecretName = componentBaseName + "-tls" // GCMx's HTTPS serving certificate
	managedResourceTimeout      = 2 * time.Minute            // Timeout for ManagedResources to become healthy or deleted
)

// Deploys the GCMx server TLS certificate to a secret and returns the name of the created secret
func (gcmx *gardenerCustomMetrics) deployServerCertificate(ctx context.Context) (*corev1.Secret, error) {
	_, found := gcmx.secretsManager.Get(v1beta1constants.SecretNameCASeed)
	if !found {
		return nil, fmt.Errorf("secret %q not found", v1beta1constants.SecretNameCASeed)
	}

	serverCertificateSecret, err := gcmx.secretsManager.Generate(
		ctx,
		&secretsutils.CertificateSecretConfig{
			Name:                        serverCertificateSecretName,
			CommonName:                  serviceName,
			DNSNames:                    kubernetesutils.DNSNamesForService(serviceName, gcmx.namespace),
			CertType:                    secretsutils.ServerCert,
			SkipPublishingCACertificate: true,
		},
		secretsmanager.SignedByCA(v1beta1constants.SecretNameCASeed, secretsmanager.UseCurrentCA),
		secretsmanager.Rotate(secretsmanager.InPlace))
	if err != nil {
		return nil, fmt.Errorf("failed to generate TLS certificate: %w", err)
	}

	return serverCertificateSecret, nil
}
