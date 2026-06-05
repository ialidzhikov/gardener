// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package discover

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	gonumgraph "gonum.org/v1/gonum/graph"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gardencorev1 "github.com/gardener/gardener/pkg/apis/core/v1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	securityv1alpha1 "github.com/gardener/gardener/pkg/apis/security/v1alpha1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	"github.com/gardener/gardener/pkg/gardenadm"
	"github.com/gardener/gardener/pkg/gardenadm/botanist"
	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	"github.com/gardener/gardener/pkg/utils/flow"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
	"github.com/gardener/gardener/pkg/utils/graph"
	kubernetesutils "github.com/gardener/gardener/pkg/utils/kubernetes"
)

// NewCommand creates a new cobra.Command.
func NewCommand(globalOpts *cmd.Options) *cobra.Command {
	opts := &Options{Options: globalOpts}

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Conveniently download Gardener configuration resources from an existing garden cluster",
		Long:  "Conveniently download Gardener configuration resources from an existing garden cluster (CloudProfile, ControllerRegistrations, ControllerDeployments, etc.)",
		Args:  cobra.NoArgs,

		Example: `# Download the configuration for a new Shoot
gardenadm discover --shoot-manifest <path-to-shoot-manifest>

# Download the configuration for an existing Shoot
gardenadm discover --shoot-name <name> --shoot-namespace <namespace>`,

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.ParseArgs(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			if err := opts.Complete(); err != nil {
				return err
			}

			return run(cmd.Context(), opts)
		},
	}

	opts.addFlags(cmd.Flags())

	return cmd
}

var (
	// NewClientSetFromFile is an alias for botanist.NewClientSetFromFile.
	// Exposed for unit testing.
	NewClientSetFromFile = botanist.NewClientSetFromFile
	// NewAferoFs is an alias for returning an afero.NewOsFs.
	// Exposed for unit testing.
	NewAferoFs = func() afero.Afero { return afero.Afero{Fs: afero.NewOsFs()} }
)

func run(ctx context.Context, opts *Options) error {
	fs := NewAferoFs()

	clientSet, err := NewClientSetFromFile(opts.Kubeconfig, kubernetes.GardenScheme)
	if err != nil {
		return fmt.Errorf("failed creating client: %w", err)
	}

	shoot, err := loadShoot(ctx, clientSet.Client(), fs, opts)
	if err != nil {
		return err
	}

	binding, err := secretBindingForShoot(ctx, clientSet.Client(), shoot)
	if err != nil {
		return fmt.Errorf("failed reading binding for shoot: %w", err)
	}

	fmt.Fprintf(opts.Out, "Computing required resources for Shoot...\n")

	g := graph.New(opts.Log, clientSet.Client(), true)
	g.HandleShootCreateOrUpdate(ctx, shoot)
	if binding != nil {
		switch b := binding.(type) {
		case *gardencorev1beta1.SecretBinding:
			g.HandleSecretBindingCreateOrUpdate(b)
		case *securityv1alpha1.CredentialsBinding:
			g.HandleCredentialsBindingCreateOrUpdate(b)
		}
	}

	var taskFns []flow.TaskFn

	g.Visit(g.Nodes(), func(n gonumgraph.Node) {
		if vertex, ok := n.(*graph.Vertex); ok {
			taskFns = append(taskFns, func(ctx context.Context) error {
				kindObject, ok := graph.VertexTypes[vertex.Type]
				if !ok {
					return fmt.Errorf("unknown vertex type %q for vertex %s/%s", vertex.Type, vertex.Namespace, vertex.Name)
				}

				obj := kindObject.NewObjectFunc()
				obj.SetName(vertex.Name)
				obj.SetNamespace(vertex.Namespace)

				return getAndExportObject(ctx, clientSet.Client(), fs, opts, kindObject.Kind, obj)
			})
		}
	})

	project, err := gardenerutils.ProjectForNamespaceFromReader(ctx, clientSet.Client(), shoot.Namespace)
	if err != nil {
		return fmt.Errorf("failed reading project: %w", err)
	}
	taskFns = append(taskFns, func(ctx context.Context) error {
		return getAndExportObject(ctx, clientSet.Client(), fs, opts, "Project", project)
	})

	existingShoot := opts.ShootName != "" && opts.ShootNamespace != ""
	if existingShoot {
		backupBucket, backupEntry, err := backupResourcesForShoot(ctx, clientSet.Client(), shoot)
		if err != nil {
			return fmt.Errorf("failed reading backup resources for shoot: %w", err)
		}
		if backupBucket != nil {
			taskFns = append(taskFns, func(ctx context.Context) error {
				return getAndExportObject(ctx, clientSet.Client(), fs, opts, "BackupBucket", backupBucket)
			})
		}
		if backupEntry != nil {
			taskFns = append(taskFns, func(ctx context.Context) error {
				return getAndExportObject(ctx, clientSet.Client(), fs, opts, "BackupEntry", backupEntry)
			})
		}
	}

	extensions, err := requiredExtensions(ctx, clientSet.Client(), shoot, opts.ManagedInfrastructure)
	if err != nil {
		return fmt.Errorf("failed computing required extensions: %w", err)
	}

	for _, extension := range extensions {
		taskFns = append(taskFns,
			func(ctx context.Context) error {
				return getAndExportObject(ctx, clientSet.Client(), fs, opts, "ControllerRegistration", extension.ControllerRegistration)
			},
			func(ctx context.Context) error {
				return getAndExportObject(ctx, clientSet.Client(), fs, opts, "ControllerDeployment", extension.ControllerDeployment)
			},
		)
	}

	fmt.Fprintf(opts.Out, "Fetching required resources for from garden cluster...\n\n")

	return flow.Parallel(taskFns...)(ctx)
}

// loadShoot returns the Shoot either from the local manifest file (for a new Shoot) or from the garden cluster
// (for an existing Shoot, when --shoot-name and --shoot-namespace are set).
func loadShoot(ctx context.Context, c client.Client, fs afero.Afero, opts *Options) (*gardencorev1beta1.Shoot, error) {
	if len(opts.ShootName) > 0 && len(opts.ShootNamespace) > 0 {
		shoot := &gardencorev1beta1.Shoot{ObjectMeta: metav1.ObjectMeta{Name: opts.ShootName, Namespace: opts.ShootNamespace}}
		if err := c.Get(ctx, client.ObjectKeyFromObject(shoot), shoot); err != nil {
			return nil, fmt.Errorf("failed getting Shoot %s from garden cluster: %w", client.ObjectKeyFromObject(shoot), err)
		}

		return shoot, nil
	}

	shoot, err := readShootManifest(fs, opts.ShootManifest)
	if err != nil {
		return nil, fmt.Errorf("failed reading shoot manifest from %q: %w", opts.ShootManifest, err)
	}

	return shoot, nil
}

var (
	versions = schema.GroupVersions([]schema.GroupVersion{gardencorev1.SchemeGroupVersion, gardencorev1beta1.SchemeGroupVersion})
	decoder  = kubernetes.GardenCodec.CodecForVersions(kubernetes.GardenSerializer, kubernetes.GardenSerializer, versions, versions)
)

func readShootManifest(fs afero.Afero, manifestPath string) (*gardencorev1beta1.Shoot, error) {
	shootManifest, err := fs.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}

	shoot := &gardencorev1beta1.Shoot{}
	if _, _, err := decoder.Decode(shootManifest, nil, shoot); err != nil {
		return nil, err
	}

	return shoot, nil
}

func secretBindingForShoot(ctx context.Context, c client.Client, shoot *gardencorev1beta1.Shoot) (client.Object, error) {
	switch {
	case shoot.Spec.SecretBindingName != nil:
		secretBinding := &gardencorev1beta1.SecretBinding{ObjectMeta: metav1.ObjectMeta{Name: *shoot.Spec.SecretBindingName, Namespace: shoot.Namespace}}
		return secretBinding, c.Get(ctx, client.ObjectKeyFromObject(secretBinding), secretBinding)

	case shoot.Spec.CredentialsBindingName != nil:
		credentialsBinding := &securityv1alpha1.CredentialsBinding{ObjectMeta: metav1.ObjectMeta{Name: *shoot.Spec.CredentialsBindingName, Namespace: shoot.Namespace}}
		return credentialsBinding, c.Get(ctx, client.ObjectKeyFromObject(credentialsBinding), credentialsBinding)

	default:
		return nil, nil
	}
}

func requiredExtensions(ctx context.Context, c client.Client, shoot *gardencorev1beta1.Shoot, managedInfrastructure bool) ([]botanist.Extension, error) {
	resources := gardenadm.Resources{Shoot: shoot}

	controllerRegistrationList := &gardencorev1beta1.ControllerRegistrationList{}
	if err := c.List(ctx, controllerRegistrationList); err != nil {
		return nil, fmt.Errorf("failed listing controllerRegistrations: %w", err)
	}
	controllerDeploymentList := &gardencorev1.ControllerDeploymentList{}
	if err := c.List(ctx, controllerDeploymentList); err != nil {
		return nil, fmt.Errorf("failed listing controllerDeployments: %w", err)
	}

	if err := meta.EachListItem(controllerRegistrationList, func(obj runtime.Object) error {
		resources.ControllerRegistrations = append(resources.ControllerRegistrations, obj.(*gardencorev1beta1.ControllerRegistration))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed adding ControllerRegistrations: %w", err)
	}

	if err := meta.EachListItem(controllerDeploymentList, func(obj runtime.Object) error {
		resources.ControllerDeployments = append(resources.ControllerDeployments, obj.(*gardencorev1.ControllerDeployment))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed adding ControllerDeployments: %w", err)
	}

	return botanist.ComputeExtensions(resources, true, managedInfrastructure)
}

func backupResourcesForShoot(ctx context.Context, c client.Client, shoot *gardencorev1beta1.Shoot) (*gardencorev1beta1.BackupBucket, *gardencorev1beta1.BackupEntry, error) {
	backupEntries := &gardencorev1beta1.BackupEntryList{}
	if err := c.List(ctx, backupEntries, client.InNamespace(shoot.Namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed listing BackupEntries in namespace %q: %w", shoot.Namespace, err)
	}

	var coreBackupEntry *gardencorev1beta1.BackupEntry
	for i := range backupEntries.Items {
		be := &backupEntries.Items[i]
		if be.Spec.ShootRef == nil {
			continue
		}
		if be.Spec.ShootRef.Name != shoot.Name || be.Spec.ShootRef.Namespace != shoot.Namespace {
			continue
		}

		if coreBackupEntry != nil {
			return nil, nil, fmt.Errorf("found more than one BackupEntry for Shoot %s/%s", shoot.Namespace, shoot.Name)
		}

		coreBackupEntry = be
	}

	if coreBackupEntry == nil {
		return nil, nil, nil
	}

	backupBucket := &gardencorev1beta1.BackupBucket{ObjectMeta: metav1.ObjectMeta{Name: coreBackupEntry.Spec.BucketName}}
	if err := c.Get(ctx, client.ObjectKeyFromObject(backupBucket), backupBucket); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("failed getting core BackupBucket %q: %w", backupBucket.Name, err)
	}

	return backupBucket, coreBackupEntry, nil
}

func getAndExportObject(ctx context.Context, c client.Client, fs afero.Afero, opts *Options, kind string, obj client.Object) error {
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed getting %s %q: %w", kind, client.ObjectKeyFromObject(obj), err)
		}
		opts.Log.V(1).Info("Object not found in garden cluster", "kind", kind, "obj", client.ObjectKeyFromObject(obj))
		return nil
	}
	return exportObject(fs, opts, kind, obj)
}

func exportObject(fs afero.Afero, opts *Options, kind string, obj client.Object) error {
	resetObject(obj)

	objYAML, err := kubernetesutils.Serialize(obj, kubernetes.GardenScheme)
	if err != nil {
		return fmt.Errorf("failed serializing %T %q: %w", obj, client.ObjectKeyFromObject(obj), err)
	}

	path := filepath.Join(opts.ConfigDir, fmt.Sprintf("%s-%s.yaml", strings.ToLower(kind), obj.GetName()))
	if err := fs.WriteFile(path, []byte(objYAML), 0600); err != nil {
		return fmt.Errorf("failed writing file to %s: %w", path, err)
	}

	fmt.Fprintf(opts.Out, "Exported %s/%s to %s\n", kind, obj.GetName(), path)
	return nil
}

func resetObject(obj client.Object) {
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetFinalizers(nil)
	obj.SetGeneration(0)
	obj.SetOwnerReferences(nil)
	obj.SetManagedFields(nil)
	obj.SetResourceVersion("")
	obj.SetSelfLink("")
	obj.SetUID("")
}
