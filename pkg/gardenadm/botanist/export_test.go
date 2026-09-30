// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package botanist

import corev1 "k8s.io/api/core/v1"

// SetOperatingSystemConfigSecret sets the unexported operatingSystemConfigSecret field for tests, mirroring the state
// left by createOperatingSystemConfigSecretForNodeAgent during bootstrap.
func (b *GardenadmBotanist) SetOperatingSystemConfigSecret(secret *corev1.Secret) {
	b.operatingSystemConfigSecret = secret
}
