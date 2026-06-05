## gardenadm discover

Conveniently download Gardener configuration resources from an existing garden cluster

### Synopsis

Conveniently download Gardener configuration resources from an existing garden cluster (CloudProfile, ControllerRegistrations, ControllerDeployments, etc.)

```
gardenadm discover [flags]
```

### Examples

```
# Download the configuration for a new Shoot
gardenadm discover --shoot-manifest <path-to-shoot-manifest>

# Download the configuration for an existing Shoot
gardenadm discover --shoot-name <name> --shoot-namespace <namespace>
```

### Options

```
  -d, --config-dir string        Path to a directory containing the Gardener configuration files for the init command, i.e., files containing resources like CloudProfile, Shoot, etc. The files must be in YAML/JSON and have .{yaml,yml,json} file extensions to be considered.
  -h, --help                     help for discover
  -k, --kubeconfig string        Path to the kubeconfig file pointing to the garden cluster
      --managed-infrastructure   Indicates whether Gardener will manage the shoot's infrastructure (network, domains, machines, etc.). Set this to true if using 'gardenadm bootstrap' for bootstrapping the shoot cluster. Set this to false if managing the infrastructure outside of Gardener. (default true)
      --shoot-manifest string    Path to a Shoot manifest file describing a new Shoot to discover resources for. Mutually exclusive with --shoot-name/--shoot-namespace.
      --shoot-name string        Name of an existing Shoot in the garden cluster to discover resources for. Mutually exclusive with --shoot-manifest. Must be set together with --shoot-namespace.
      --shoot-namespace string   Namespace of an existing Shoot in the garden cluster to discover resources for. Mutually exclusive with --shoot-manifest. Must be set together with --shoot-name.
```

### Options inherited from parent commands

```
      --log-format string   The format for the logs. Must be one of [json text] (default "text")
      --log-level string    The level/severity for the logs. Must be one of [debug info error] (default "info")
```

### SEE ALSO

* [gardenadm](gardenadm.md)	 - gardenadm bootstraps and manages self-hosted shoot clusters in the Gardener project.

