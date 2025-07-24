// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package shoothibernation_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/gardener/gardener/test/e2e"
	"github.com/gardener/gardener/test/e2e/gardener"
	. "github.com/gardener/gardener/test/e2e/gardener/shoot/spec"
)

func init() {
	RegisterShootFlags()
}

var _ = Describe("Shoot hibernation testing", Ordered, func() {
	var s *gardener.ShootContext
	e2e.BeforeTestSetup(func() {
		s = &gardener.ShootContext{
			TestContext: *gardener.NewTestContext(),
		}
	})

	ItShouldCreateShoot(s)
	ItShouldHibernateShoot(s)
	ItShouldWaitForShootToBeReconciledAndHealthy(s)
})
