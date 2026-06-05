// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package shared

import (
	"fmt"

	"github.com/spf13/pflag"

	"github.com/gardener/gardener/pkg/gardenadm/cmd"
)

// CommonOptions contains the options shared by all `gardenadm discover` subcommands.
type CommonOptions struct {
	*cmd.Options
	cmd.ManifestOptions

	// Kubeconfig is the path to the kubeconfig file pointing to the garden cluster.
	Kubeconfig string
	// ManagedInfrastructure indicates whether Gardener will manage the shoot's infrastructure (network, domains,
	// machines, etc.). Set this to true if using 'gardenadm bootstrap' for bootstrapping the shoot cluster. Set this to
	// false if managing the infrastructure outside of Gardener.
	ManagedInfrastructure bool
}

// ParseArgs parses the common arguments to the options.
func (o *CommonOptions) ParseArgs(args []string) error {
	if err := cmd.DefaultKubeconfig(&o.Kubeconfig); err != nil {
		return fmt.Errorf("could not default kubeconfig: %w", err)
	}

	return o.ManifestOptions.ParseArgs(args)
}

// Validate validates the common options.
func (o *CommonOptions) Validate() error {
	if len(o.Kubeconfig) == 0 {
		return fmt.Errorf("must provide a path to a garden cluster kubeconfig")
	}

	return o.ManifestOptions.Validate()
}

// Complete completes the common options.
func (o *CommonOptions) Complete() error {
	return o.ManifestOptions.Complete()
}

// AddFlags adds the common flags to the given flag set.
func (o *CommonOptions) AddFlags(fs *pflag.FlagSet) {
	o.ManifestOptions.AddFlags(fs)
	fs.StringVarP(&o.Kubeconfig, "kubeconfig", "k", "", "Path to the kubeconfig file pointing to the garden cluster")
	fs.StringVar(&o.ShootManifest, "shoot-manifest", "", "Path to a Shoot manifest file describing a new Shoot to discover resources for. "+
		"Mutually exclusive with --shoot-name/--shoot-namespace.")
	fs.StringVar(&o.ShootName, "shoot-name", "", "Name of an existing Shoot in the garden cluster to discover resources for. "+
		"Mutually exclusive with --shoot-manifest. Must be set together with --shoot-namespace.")
	fs.StringVar(&o.ShootNamespace, "shoot-namespace", "", "Namespace of an existing Shoot in the garden cluster to discover resources for. "+
		"Mutually exclusive with --shoot-manifest. Must be set together with --shoot-name.")
	fs.BoolVar(&o.ManagedInfrastructure, "managed-infrastructure", true, "Indicates whether Gardener will manage the shoot's infrastructure (network, domains, machines, etc.). Set this to true if using 'gardenadm bootstrap' for bootstrapping the shoot cluster. Set this to false if managing the infrastructure outside of Gardener.")
}
