// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package botanist_test

import (
	"context"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	fakekubernetes "github.com/gardener/gardener/pkg/client/kubernetes/fake"
	. "github.com/gardener/gardener/pkg/gardenadm/botanist"
	"github.com/gardener/gardener/pkg/gardenlet/operation"
	botanistpkg "github.com/gardener/gardener/pkg/gardenlet/operation/botanist"
	shootpkg "github.com/gardener/gardener/pkg/gardenlet/operation/shoot"
	. "github.com/gardener/gardener/pkg/utils/test/matchers"
)

const (
	oscSecretName      = "gardener-node-agent-control-plane-abc123"
	oscSecretNamespace = "kube-system"
	priorNodeName      = "prior-node"
)

var _ = Describe("Restore", func() {
	var (
		ctx = context.Background()
		b   *GardenadmBotanist

		realClient client.Client
	)

	BeforeEach(func() {
		realClient = fakeclient.NewClientBuilder().WithScheme(kubernetes.SeedScheme).Build()

		b = &GardenadmBotanist{
			Botanist: &botanistpkg.Botanist{
				Operation: &operation.Operation{
					Logger: logr.Discard(),
					Shoot:  &shootpkg.Shoot{},
					SeedClientSet: fakekubernetes.
						NewClientSetBuilder().
						WithClient(realClient).
						WithRESTConfig(&rest.Config{}).
						Build(),
				},
			},
		}

		b.SetOperatingSystemConfigSecret(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: oscSecretName, Namespace: oscSecretNamespace},
		})
	})

	Describe("#DeleteStaleOperatingSystemConfigSecret", func() {
		It("should delete the stale OperatingSystemConfig secret restored from the ETCD snapshot", func() {
			restoredSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: oscSecretName, Namespace: oscSecretNamespace},
				Data:       map[string][]byte{"osc.yaml": []byte("managed-content")},
			}
			Expect(realClient.Create(ctx, restoredSecret)).To(Succeed())

			Expect(b.DeleteStaleOperatingSystemConfigSecret(ctx, realClient)).To(Succeed())

			Expect(realClient.Get(ctx, client.ObjectKeyFromObject(restoredSecret), &corev1.Secret{})).To(BeNotFoundError())
		})

		It("should succeed when the secret is absent (IgnoreNotFound)", func() {
			Expect(b.DeleteStaleOperatingSystemConfigSecret(ctx, realClient)).To(Succeed())
		})

		It("should error when the OperatingSystemConfig secret was not computed yet", func() {
			b.SetOperatingSystemConfigSecret(nil)

			Expect(b.DeleteStaleOperatingSystemConfigSecret(ctx, realClient)).To(MatchError(ContainSubstring("operating system config secret is nil")))
		})
	})

	Describe("#FinalizeManagedResources", func() {
		It("should remove the finalizers from and delete all ManagedResources", func() {
			managedResource := &resourcesv1alpha1.ManagedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "shoot-gardener-node-agent",
					Namespace:  oscSecretNamespace,
					Finalizers: []string{"resources.gardener.cloud/gardener-resource-manager"},
				},
			}
			Expect(realClient.Create(ctx, managedResource)).To(Succeed())

			Expect(b.FinalizeManagedResources(ctx, realClient)).To(Succeed())

			Expect(realClient.Get(ctx, client.ObjectKeyFromObject(managedResource), &resourcesv1alpha1.ManagedResource{})).To(BeNotFoundError())
		})

		It("should be a no-op when there are no ManagedResources", func() {
			Expect(b.FinalizeManagedResources(ctx, realClient)).To(Succeed())
		})
	})

	Describe("#DeletePriorNodeAndPodsRunningOnIt", func() {
		It("should delete the prior Node and only the Pods running on it", func() {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: priorNodeName}}
			Expect(realClient.Create(ctx, node)).To(Succeed())

			priorPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "prior-pod", Namespace: oscSecretNamespace}, Spec: corev1.PodSpec{NodeName: priorNodeName}}
			Expect(realClient.Create(ctx, priorPod)).To(Succeed())

			otherPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: oscSecretNamespace}, Spec: corev1.PodSpec{NodeName: "some-other-node"}}
			Expect(realClient.Create(ctx, otherPod)).To(Succeed())

			Expect(b.DeletePriorNodeAndPodsRunningOnIt(ctx, realClient, priorNodeName)).To(Succeed())

			Expect(realClient.Get(ctx, client.ObjectKeyFromObject(node), &corev1.Node{})).To(BeNotFoundError())
			Expect(realClient.Get(ctx, client.ObjectKeyFromObject(priorPod), &corev1.Pod{})).To(BeNotFoundError())
			Expect(realClient.Get(ctx, client.ObjectKeyFromObject(otherPod), &corev1.Pod{})).To(Succeed())
		})

		It("should succeed when the prior Node is absent (IgnoreNotFound)", func() {
			Expect(b.DeletePriorNodeAndPodsRunningOnIt(ctx, realClient, priorNodeName)).To(Succeed())
		})

		It("should error when priorNodeName is empty", func() {
			Expect(b.DeletePriorNodeAndPodsRunningOnIt(ctx, realClient, "")).To(MatchError(ContainSubstring("must not be empty")))
		})
	})
})
