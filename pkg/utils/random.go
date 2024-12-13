// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	cryptorand "crypto/rand"
	"math/big"
	mathrand "math/rand/v2"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GenerateRandomString uses crypto/rand to generate a random string of the specified length <n>.
// The set of allowed characters is [0-9a-zA-Z], thus no special characters are included in the output.
// Returns error if there was a problem during the random generation.
func GenerateRandomString(n int) (string, error) {
	allowedCharacters := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	return GenerateRandomStringFromCharset(n, allowedCharacters)
}

// GenerateRandomStringFromCharset generates a cryptographically secure random string of the specified length <n>.
// The set of allowed characters can be specified. Returns error if there was a problem during the random generation.
func GenerateRandomStringFromCharset(n int, allowedCharacters string) (string, error) {
	output := make([]byte, n)
	maximum := new(big.Int).SetInt64(int64(len(allowedCharacters)))

	for i := range output {
		randomCharacter, err := cryptorand.Int(cryptorand.Reader, maximum)
		if err != nil {
			return "", err
		}
		output[i] = allowedCharacters[randomCharacter.Int64()]
	}
	return string(output), nil
}

// RandomDuration takes a time.Duration and computes a non-negative pseudo-random duration in [0,max).
// It returns 0ns if max is <= 0ns.
func RandomDuration(maximum time.Duration) time.Duration {
	if maximum.Nanoseconds() <= 0 {
		return time.Duration(0)
	}
	return time.Duration(mathrand.N(maximum.Nanoseconds())) // #nosec: G404 -- No cryptographic context.
}

// RandomDurationWithMetaDuration takes a *metav1.Duration and computes a non-negative pseudo-random duration in [0,max).
// It returns 0ns if max is nil or <= 0ns.
func RandomDurationWithMetaDuration(maximum *metav1.Duration) time.Duration {
	if maximum == nil {
		return time.Duration(0)
	}
	return RandomDuration(maximum.Duration)
}
