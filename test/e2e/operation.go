// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"time"

	. "github.com/gardener/gardener/test/e2e/gardener"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
)

// ItShouldEventuallyNotHaveOperationAnnotation checks if the given object does not have the gardener operation annotation set
func ItShouldEventuallyNotHaveOperationAnnotation(s TestContext, obj client.Object) {
	GinkgoHelper()
	It("Should not have operation annotation", func(ctx SpecContext) {
		fmt.Printf("s.GardenKomega = %+v \n", s.GardenKomega)
		fmt.Printf("obj = %+v \n", obj)
		Eventually(ctx, s.GardenKomega.Object(obj)).WithPolling(2 * time.Second).Should(
			HaveField("ObjectMeta.Annotations", Not(HaveKey(v1beta1constants.GardenerOperation))))
	}, SpecTimeout(time.Minute))
}
