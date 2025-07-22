// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package referencedresourcelabels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	admissionwebhook "github.com/gardener/gardener/pkg/admissioncontroller/webhook/admission"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var decoder runtime.Decoder

func init() {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	decoder = serializer.NewCodecFactory(scheme).UniversalDeserializer()
}

type Handler struct {
	Client  client.Client
	Decoder admission.Decoder
}

func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	requestGK := schema.GroupKind{Group: req.Kind.Group, Kind: req.Kind.Kind}

	switch requestGK {
	case schema.GroupKind{Group: corev1.GroupName, Kind: "Secret"}:
		obj := &corev1.Secret{}
		if err := h.Decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusUnprocessableEntity, err)
		}

		return h.adminReferencedResource(ctx, req, obj)
	case schema.GroupKind{Group: corev1.GroupName, Kind: "ConfigMap"}:
		obj := &corev1.ConfigMap{}
		if err := h.Decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusUnprocessableEntity, err)
		}

		return h.adminReferencedResource(ctx, req, obj)
	}

	return admissionwebhook.Allowed("resource is neither of type *corev1.Secret nor *corev1.ConfigMap")
}

func (h *Handler) adminReferencedResource(ctx context.Context, req admission.Request, obj client.Object) admission.Response {
	namespace := obj.GetNamespace()

	shootList := &gardencorev1beta1.ShootList{}
	if err := h.Client.List(ctx, shootList, client.InNamespace(namespace)); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to list Shoots: %w", err))
	}

	extensionTypes := sets.New[string]()
	for _, shoot := range shootList.Items {
		i := slices.IndexFunc(shoot.Spec.Resources, func(resource gardencorev1beta1.NamedResourceReference) bool {
			return resource.ResourceRef.Kind == obj.GetObjectKind().GroupVersionKind().Kind &&
				resource.ResourceRef.APIVersion == obj.GetObjectKind().GroupVersionKind().GroupVersion().String() &&
				resource.ResourceRef.Name == obj.GetName()
		})

		if i == -1 {
			continue
		}

		resourceName := shoot.Spec.Resources[i].Name
		for _, extension := range shoot.Spec.Extensions {
			for _, resourceMount := range extension.ResourceMounts {
				if resourceMount.Name == resourceName {
					extensionTypes.Insert(extension.Type)
				}
			}
		}
	}

	if extensionTypes.Len() > 0 {
		maintainLabels(obj, extensionTypes.UnsortedList()...)
	}

	marshalled, err := json.Marshal(obj)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshalled)
}

func maintainLabels(obj client.Object, extensionTypes ...string) {
	labels := obj.GetLabels()
	for k := range obj.GetLabels() {
		if strings.HasPrefix(k, v1beta1constants.LabelExtensionExtensionTypePrefix) {
			delete(labels, k)
		}
	}

	if labels == nil {
		labels = make(map[string]string, len(extensionTypes))
	}
	for _, extensionType := range extensionTypes {
		labels[v1beta1constants.LabelExtensionExtensionTypePrefix+extensionType] = "true"
	}

	obj.SetLabels(labels)
}
