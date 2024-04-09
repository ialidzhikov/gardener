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

package apiserver_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	vpaautoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	. "github.com/gardener/gardener/pkg/component/kubernetes/apiserver"
	. "github.com/gardener/gardener/pkg/utils/test/matchers"
)

var _ = Describe("BilinearPodAutoscaler", func() {
	const (
		containerNameApiserver = "kube-apiserver"
		deploymentName         = "test-deployment"
		namespaceName          = "test-namespace"
		hpaName                = deploymentName + "-bipa"
		vpaName                = hpaName
	)

	var (
		fakeClient client.Client
		ctx        = context.Background()

		bipa      *BilinearPodAutoscaler
		consistOf func(object ...client.Object) types.GomegaMatcher

		expectedVPA *vpaautoscalingv1.VerticalPodAutoscaler

		hpaFor = func(minReplicas, maxReplicas int32) *autoscalingv2.HorizontalPodAutoscaler {
			return &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:            hpaName,
					Namespace:       namespaceName,
					Labels:          map[string]string{v1beta1constants.LabelRole: v1beta1constants.LabelAPIServer + "-hpa"},
					ResourceVersion: "1",
				},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					MinReplicas: ptr.To[int32](minReplicas),
					MaxReplicas: maxReplicas,
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
					},
					Behavior: &autoscalingv2.HorizontalPodAutoscalerBehavior{
						ScaleDown: &autoscalingv2.HPAScalingRules{
							StabilizationWindowSeconds: ptr.To[int32](900),
						},
					},
					Metrics: []autoscalingv2.MetricSpec{
						{
							Type: autoscalingv2.PodsMetricSourceType,
							Pods: &autoscalingv2.PodsMetricSource{
								Metric: autoscalingv2.MetricIdentifier{Name: "shoot:apiserver_request_total:sum"},
								Target: autoscalingv2.MetricTarget{AverageValue: ptr.To(resource.MustParse("300")), Type: autoscalingv2.AverageValueMetricType},
							},
						},
					},
				},
			}
		}

		// Creates empty control plane objects which superficially mirror the objects deployed by BIPA reconciliation
		createBilinearPodAutoscalerObjects = func() {
			hpa := &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hpaName,
					Namespace: namespaceName,
				},
			}
			vpa := &vpaautoscalingv1.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vpaName,
					Namespace: namespaceName,
				},
			}
			Expect(fakeClient.Create(ctx, hpa)).To(Succeed())
			Expect(fakeClient.Create(ctx, vpa)).To(Succeed())

			mr := &v1alpha1.ManagedResource{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespaceName, Name: "gardener-custom-metrics"},
			}
			Expect(fakeClient.Create(ctx, mr)).To(Succeed())
		}
	)

	BeforeEach(func() {
		fakeClient = fakeclient.NewClientBuilder().WithScheme(kubernetes.SeedScheme).Build()

		bipa = NewBilinearPodAutoscaler(namespaceName, deploymentName)
		consistOf = NewManagedResourceConsistOfObjectsMatcher(fakeClient)

		expectedVPA = &vpaautoscalingv1.VerticalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:            vpaName,
				Namespace:       namespaceName,
				Labels:          map[string]string{v1beta1constants.LabelRole: v1beta1constants.LabelAPIServer + "-vpa"},
				ResourceVersion: "1",
			},
			Spec: vpaautoscalingv1.VerticalPodAutoscalerSpec{
				TargetRef: &autoscalingv1.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deploymentName,
				},
				UpdatePolicy: &vpaautoscalingv1.PodUpdatePolicy{
					MinReplicas: ptr.To[int32](1),
					UpdateMode:  ptr.To(vpaautoscalingv1.UpdateModeAuto),
				},
				ResourcePolicy: &vpaautoscalingv1.PodResourcePolicy{
					ContainerPolicies: []vpaautoscalingv1.ContainerResourcePolicy{
						{
							ContainerName: containerNameApiserver,
							Mode:          ptr.To(vpaautoscalingv1.ContainerScalingModeAuto),
							MinAllowed: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("400M"),
							},
							MaxAllowed: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("8"),
								corev1.ResourceMemory: resource.MustParse("25G"),
							},
							ControlledValues: ptr.To(vpaautoscalingv1.ContainerControlledValuesRequestsOnly),
						},
					},
				},
			},
		}
	})

	Describe("#Reconcile", func() {
		It("should deploy the correct resources to the shoot control plane", func() {
			clusterRole := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gardener.cloud:monitoring:gardener-custom-metrics-target",
				},
				Rules: []rbacv1.PolicyRule{
					{
						NonResourceURLs: []string{"/metrics"},
						Verbs:           []string{"get"},
					},
				},
			}
			clusterRoleBinding := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "gardener.cloud:monitoring:gardener-custom-metrics-target",
					Annotations: map[string]string{"resources.gardener.cloud/delete-on-invalid-update": "true"},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "gardener.cloud:monitoring:gardener-custom-metrics-target",
				},
				Subjects: []rbacv1.Subject{{
					Kind:      "ServiceAccount",
					Name:      "gardener-custom-metrics",
					Namespace: "kube-system",
				}},
			}

			parameters := &DesiredStateParameters{
				MinReplicaCount:        1,
				MaxReplicaCount:        4,
				ContainerNameApiserver: containerNameApiserver,
			}
			Expect(bipa.Reconcile(ctx, fakeClient, parameters)).To(Succeed())

			actualHPA := autoscalingv2.HorizontalPodAutoscaler{}
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: hpaName}, &actualHPA)).
				To(Succeed())
			Expect(&actualHPA).
				To(DeepEqual(hpaFor(parameters.MinReplicaCount, parameters.MaxReplicaCount)))

			actualVPA := vpaautoscalingv1.VerticalPodAutoscaler{}
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: vpaName}, &actualVPA)).
				To(Succeed())
			Expect(&actualVPA).To(DeepEqual(expectedVPA))

			actualMr := &v1alpha1.ManagedResource{}
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: "gardener-custom-metrics"}, actualMr)).
				To(Succeed())
			Expect(actualMr).To(consistOf(
				clusterRole,
				clusterRoleBinding,
			))
		})
	})

	Describe("#Delete", func() {
		It("should remove the respective resources in the shoot control plane", func() {
			createBilinearPodAutoscalerObjects()

			Expect(bipa.Delete(ctx, fakeClient)).To(Succeed())

			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: hpaName}, &autoscalingv2.HorizontalPodAutoscaler{})).To(BeNotFoundError())
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: hpaName}, &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: "gardener-custom-metrics"}, &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())
		})

		It("should not fail if resources are missing on the seed", func() {
			Expect(bipa.Delete(ctx, fakeClient)).To(Succeed())

			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: hpaName}, &autoscalingv2.HorizontalPodAutoscaler{})).To(BeNotFoundError())
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: hpaName}, &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())
			Expect(fakeClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: "gardener-custom-metrics"}, &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())
		})
	})
})
