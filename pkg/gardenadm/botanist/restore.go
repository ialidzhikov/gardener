// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package botanist

import (
	"context"
	"fmt"
	"strings"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/utils"
	kubernetesutils "github.com/gardener/gardener/pkg/utils/kubernetes"
)

// DeleteStaleOperatingSystemConfigSecret deletes the gardener-node-agent OperatingSystemConfig Secret restored from the
// ETCD snapshot. It carries the *managed* etcd static-pod manifests under the content-independent Secret name, so
// deleting it lets the subsequent MigrateSecrets task reinstall the *bootstrap*-content Secret under the same name -
// mirroring the healthy lineage of `gardenadm init`. The later etcd-druid transition then rewrites the Secret with
// managed content.
func (b *GardenadmBotanist) DeleteStaleOperatingSystemConfigSecret(ctx context.Context, realClient client.Client) error {
	if b.operatingSystemConfigSecret == nil {
		return fmt.Errorf("operating system config secret is nil, make sure to call createOperatingSystemConfigSecretForNodeAgent() first")
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      b.operatingSystemConfigSecret.Name,
		Namespace: b.operatingSystemConfigSecret.Namespace,
	}}
	return client.IgnoreNotFound(realClient.Delete(ctx, secret))
}

// FinalizeManagedResources removes the finalizers from and deletes all ManagedResources restored from the ETCD snapshot,
// and waits until they are gone. During bootstrap gardener-resource-manager is not yet running, so the finalizers have
// to be removed explicitly. It must run before DeleteStaleOperatingSystemConfigSecret (see there).
func (b *GardenadmBotanist) FinalizeManagedResources(ctx context.Context, realClient client.Client) error {
	managedResourceList := &resourcesv1alpha1.ManagedResourceList{}
	if err := realClient.List(ctx, managedResourceList); err != nil {
		return fmt.Errorf("failed listing ManagedResources: %w", err)
	}

	for _, managedResource := range managedResourceList.Items {
		obj := managedResource.DeepCopy()
		obj.SetFinalizers(nil)

		b.Logger.Info("Removing ManagedResource finalizers", "managedResource", client.ObjectKeyFromObject(obj))
		if err := realClient.Update(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed updating ManagedResource %s: %w", client.ObjectKeyFromObject(obj), err)
		}

		b.Logger.Info("Deleting ManagedResource", "managedResource", client.ObjectKeyFromObject(obj))
		if err := realClient.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting ManagedResource %s: %w", client.ObjectKeyFromObject(obj), err)
		}
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	b.Logger.Info("Waiting for ManagedResources to be cleaned up")
	if err := kubernetesutils.WaitUntilResourcesDeleted(ctxWithTimeout, realClient, managedResourceList, 10*time.Second); err != nil {
		return fmt.Errorf("failed waiting until ManagedResources are cleaned up: %w", err)
	}

	return nil
}

// DeletePriorNodeAndPodsRunningOnIt deletes the prior control plane Node (the one being replaced during restore) and
// force-deletes all Pods that were scheduled onto it.
func (b *GardenadmBotanist) DeletePriorNodeAndPodsRunningOnIt(ctx context.Context, realClient client.Client, priorNodeName string) error {
	if priorNodeName == "" {
		// Guard against an empty node name: it would delete a Node named "" and, since unscheduled Pods have an empty
		// .spec.NodeName, force-delete every Pending Pod in the cluster. Callers must pass the prior node name.
		return fmt.Errorf("priorNodeName must not be empty")
	}

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: priorNodeName}}

	b.Logger.Info("Deleting Node", "node", client.ObjectKeyFromObject(node))
	if err := realClient.Delete(ctx, node); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed deleting Node %s: %w", client.ObjectKeyFromObject(node), err)
	}

	podList := &corev1.PodList{}
	if err := realClient.List(ctx, podList); err != nil {
		return fmt.Errorf("failed listing Pods: %w", err)
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != priorNodeName {
			continue
		}

		b.Logger.Info("Force deleting Pod", "pod", client.ObjectKeyFromObject(&pod), "nodeName", pod.Spec.NodeName)
		options := &client.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0), PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)}
		if err := realClient.Delete(ctx, pod.DeepCopy(), options); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed force deleting Pod %s: %w", client.ObjectKeyFromObject(&pod), err)
		}
	}

	return nil
}

// DeleteNodeAgentCertificateSigningRequests deletes the gardener-node-agent client CSRs restored from the etcd
// snapshot. A CSR is considered a gardener-node-agent CSR if it targets the kube-apiserver-client signer and its
// embedded x509 CommonName carries the gardener-node-agent user-name prefix (the same predicate the init flow's
// approval step uses). Removing them ensures the approval step only ever sees the CSR created during the current run.
func (b *GardenadmBotanist) DeleteNodeAgentCertificateSigningRequests(ctx context.Context, realClient client.Client) error {
	csrList := &certificatesv1.CertificateSigningRequestList{}
	if err := realClient.List(ctx, csrList); err != nil {
		return fmt.Errorf("failed listing CertificateSigningRequests: %w", err)
	}

	for i := range csrList.Items {
		csr := &csrList.Items[i]
		if csr.Spec.SignerName != certificatesv1.KubeAPIServerClientSignerName {
			continue
		}

		x509cr, err := utils.DecodeCertificateRequest(csr.Spec.Request)
		if err != nil {
			return fmt.Errorf("failed decoding CertificateSigningRequest %s: %w", client.ObjectKeyFromObject(csr), err)
		}
		if !strings.HasPrefix(x509cr.Subject.CommonName, v1beta1constants.NodeAgentUserNamePrefix) {
			continue
		}

		b.Logger.Info("Deleting gardener-node-agent CertificateSigningRequest", "certificateSigningRequest", client.ObjectKeyFromObject(csr))
		if err := realClient.Delete(ctx, csr); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting CertificateSigningRequest %s: %w", client.ObjectKeyFromObject(csr), err)
		}
	}

	return nil
}
