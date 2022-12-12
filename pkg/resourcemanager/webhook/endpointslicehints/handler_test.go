// Copyright (c) 2022 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
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

package endpointslicehints_test

import (
	"context"

	"github.com/gardener/gardener/pkg/logger"
	. "github.com/gardener/gardener/pkg/resourcemanager/webhook/endpointslicehints"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/utils/pointer"
	logzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var _ = Describe("Handler", func() {
	var (
		ctx     = context.TODO()
		log     logr.Logger
		handler *Handler

		endpointSlice *discoveryv1.EndpointSlice
	)

	BeforeEach(func() {
		ctx = admission.NewContextWithRequest(ctx, admission.Request{})
		log = logger.MustNewZapLogger(logger.InfoLevel, logger.FormatJSON, logzap.WriteTo(GinkgoWriter))
		handler = &Handler{Logger: log}

		endpointSlice = &discoveryv1.EndpointSlice{}
	})

	Describe("#Default", func() {
		It("should return err when the object is not EndpointSlice", func() {
			Expect(handler.Default(ctx, &corev1.Pod{})).To(MatchError("expected *discoveryv1.EndpointSlice but got *v1.Pod"))
		})

		It("should not default the hints for endpoint without a zone", func() {
			endpointSlice.Endpoints = []discoveryv1.Endpoint{
				{
					Zone: nil,
				},
				{
					Zone: pointer.String(""),
				},
				{
					Zone: pointer.String("europe-1c"),
				},
			}

			Expect(handler.Default(ctx, endpointSlice)).To(Succeed())
			Expect(endpointSlice).To(Equal(&discoveryv1.EndpointSlice{
				Endpoints: []discoveryv1.Endpoint{
					{
						Hints: nil,
						Zone:  nil,
					},
					{
						Hints: nil,
						Zone:  pointer.String(""),
					},
					{
						Hints: &discoveryv1.EndpointHints{
							ForZones: []discoveryv1.ForZone{{Name: "europe-1c"}},
						},
						Zone: pointer.String("europe-1c"),
					},
				},
			}))
		})

		It("should default the hints when endpoint has a zone", func() {
			endpointSlice.Endpoints = []discoveryv1.Endpoint{
				{
					Zone: pointer.String("europe-1a"),
				},
				{
					Zone: pointer.String("europe-1b"),
				},
				{
					Zone: pointer.String("europe-1c"),
				},
			}

			Expect(handler.Default(ctx, endpointSlice)).To(Succeed())
			Expect(endpointSlice).To(Equal(&discoveryv1.EndpointSlice{
				Endpoints: []discoveryv1.Endpoint{
					{
						Hints: &discoveryv1.EndpointHints{
							ForZones: []discoveryv1.ForZone{{Name: "europe-1a"}},
						},
						Zone: pointer.String("europe-1a"),
					},
					{
						Hints: &discoveryv1.EndpointHints{
							ForZones: []discoveryv1.ForZone{{Name: "europe-1b"}},
						},
						Zone: pointer.String("europe-1b"),
					},
					{
						Hints: &discoveryv1.EndpointHints{
							ForZones: []discoveryv1.ForZone{{Name: "europe-1c"}},
						},
						Zone: pointer.String("europe-1c"),
					},
				},
			}))
		})
	})
})
