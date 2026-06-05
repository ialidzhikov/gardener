// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package discover_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/gardener/gardener/pkg/gardenadm/cmd/discover"
)

var _ = Describe("Options", func() {
	var (
		options *Options
	)

	BeforeEach(func() {
		options = &Options{}
	})

	Describe("#ParseArgs", func() {
		It("should set the kubeconfig", func() {
			Expect(os.Setenv("KUBECONFIG", "kubeconfig")).To(Succeed())

			Expect(options.ParseArgs(nil)).To(Succeed())

			Expect(options.Kubeconfig).To(Equal("kubeconfig"))
		})
	})

	Describe("#Validate", func() {
		It("should pass for valid options (new Shoot via manifest)", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootManifest = "some-path-to-shoot-manifest"
			options.ConfigDir = "some-path-to-config-dir"

			Expect(options.Validate()).To(Succeed())
		})

		It("should pass for valid options (existing Shoot via --shoot-name/--shoot-namespace)", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootName = "test-shoot"
			options.ShootNamespace = "garden-test"
			options.ConfigDir = "some-path-to-config-dir"

			Expect(options.Validate()).To(Succeed())
		})

		It("should fail when neither shoot manifest nor --shoot-name/--shoot-namespace are set", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide either --shoot-manifest or both --shoot-name and --shoot-namespace")))
		})

		It("should fail when both --shoot-manifest and --shoot-name are set (mutually exclusive)", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootManifest = "some-path-to-shoot-manifest"
			options.ShootName = "test-shoot"
			options.ShootNamespace = "garden-test"

			Expect(options.Validate()).To(MatchError(ContainSubstring("must not provide both --shoot-manifest and --shoot-name/--shoot-namespace")))
		})

		It("should fail when both --shoot-manifest and --shoot-namespace are set (mutually exclusive)", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootManifest = "some-path-to-shoot-manifest"
			options.ShootNamespace = "garden-test"

			Expect(options.Validate()).To(MatchError(ContainSubstring("must not provide both --shoot-manifest and --shoot-name/--shoot-namespace")))
		})

		It("should fail when --shoot-namespace is set but --shoot-name is missing", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootNamespace = "garden-test"
			options.ConfigDir = "some-path-to-config-dir"

			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide --shoot-name when --shoot-namespace is set")))
		})

		It("should fail when --shoot-name is set but --shoot-namespace is missing", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootName = "test-shoot"
			options.ConfigDir = "some-path-to-config-dir"

			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide --shoot-namespace when --shoot-name is set")))
		})

		It("should fail because config dir path is not set (new Shoot)", func() {
			options.ShootManifest = "some-path-to-shoot-manifest"
			options.Kubeconfig = "some-path-to-kubeconfig"

			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide a path to a config directory")))
		})

		It("should fail because config dir path is not set (existing Shoot)", func() {
			options.Kubeconfig = "some-path-to-kubeconfig"
			options.ShootName = "test-shoot"
			options.ShootNamespace = "garden-test"

			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide a path to a config directory")))
		})
	})

	Describe("#Complete", func() {
		It("should return nil", func() {
			Expect(options.Complete()).To(Succeed())
		})

		It("should default the config dir from the shoot manifest path", func() {
			options.ShootManifest = "foo/bar/baz.yaml"
			Expect(options.Complete()).To(Succeed())
			Expect(options.ConfigDir).To(Equal("foo/bar"))
		})

		It("should not default the config dir when explicitly specified", func() {
			options.ShootManifest = "foo/bar/baz.yaml"
			options.ConfigDir = "baz"
			Expect(options.Complete()).To(Succeed())
			Expect(options.ConfigDir).To(Equal("baz"))
		})
	})
})
