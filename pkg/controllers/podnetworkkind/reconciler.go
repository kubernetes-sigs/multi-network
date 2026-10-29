/*
Copyright (c) 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package podnetworkkind

import (
	"context"
	"errors"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
	v1alpha1client "sigs.k8s.io/multi-network-api/pkg/client/clientset/versioned/typed/apis/v1alpha1"
	v1alpha1networkkindlisters "sigs.k8s.io/multi-network-api/pkg/client/listers/apis/v1alpha1"
	"sigs.k8s.io/multi-network/pkg/ruleswatcher"
)

// PodNetworkKindReconciler reconciles PodNetworkKind resources to ensure their implementation types exist and are ready.
type PodNetworkKindReconciler struct {
	customResourceDefinitionIndexer cache.Indexer
	podNetworkKindIndexer           cache.Indexer
	podNetworkKindLister            v1alpha1networkkindlisters.PodNetworkKindLister
	podNetworkKindClient            v1alpha1client.PodNetworkKindInterface
	ruleWatcher                     *ruleswatcher.Watcher
	clientset                       clientset.Interface
}

// NewPodNetworkKindReconciler creates a new PodNetworkKindReconciler.
func NewPodNetworkKindReconciler(
	customResourceDefinitionIndexer cache.Indexer,
	podNetworkKindIndexer cache.Indexer,
	podNetworkKindLister v1alpha1networkkindlisters.PodNetworkKindLister,
	podNetworkKindClient v1alpha1client.PodNetworkKindInterface,
	ruleWatcher *ruleswatcher.Watcher,
	clientset clientset.Interface,
) *PodNetworkKindReconciler {
	return &PodNetworkKindReconciler{
		customResourceDefinitionIndexer: customResourceDefinitionIndexer,
		podNetworkKindIndexer:           podNetworkKindIndexer,
		podNetworkKindLister:            podNetworkKindLister,
		podNetworkKindClient:            podNetworkKindClient,
		ruleWatcher:                     ruleWatcher,
		clientset:                       clientset,
	}
}

// Reconcile evaluates the specified PodNetworkKind and updates its ImplementationTypeReady condition.
func (pnkr *PodNetworkKindReconciler) Reconcile(ctx context.Context, podNetworkKindName string) error {
	podNetworkKind, err := pnkr.podNetworkKindLister.Get(podNetworkKindName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get PodNetworkKind: %w", err)
	}

	group := podNetworkKind.Spec.ImplementationType.Group
	kind := podNetworkKind.Spec.ImplementationType.Kind

	plural, found, err := pnkr.findResourceType(group, kind)
	if err != nil {
		return fmt.Errorf("failed to find resource type: %w", err)
	}

	conflict, err := pnkr.hasTypeConflict(podNetworkKind)
	if err != nil {
		return fmt.Errorf("failed to check for type conflict: %w", err)
	}

	if pnkr.setImplementationTypeReadyCondition(podNetworkKind, group, plural, found, conflict) {
		_, err := pnkr.podNetworkKindClient.UpdateStatus(ctx, podNetworkKind, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update PodNetworkKind status: %w", err)
		}
	}

	return nil
}

func (pnkr *PodNetworkKindReconciler) findResourceType(group, kind string) (string, bool, error) {
	objs, err := pnkr.customResourceDefinitionIndexer.ByIndex(
		groupKindCustomResourceDefinitionIndex,
		schema.GroupKind{Group: group, Kind: kind}.String(),
	)
	if err != nil {
		return "", false, fmt.Errorf("failed to get CustomResourceDefinition: %w", err)
	}

	// In Kubernetes, apiextensions-apiserver enforces Group/Kind uniqueness across CRDs,
	// so more than one CRD matching the same GroupKind should be impossible in practice.
	if len(objs) > 1 {
		return "", false, fmt.Errorf("multiple CustomResourceDefinitions found for GroupKind %s/%s", group, kind)
	}

	if len(objs) == 1 {
		crd, ok := objs[0].(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return "", false, fmt.Errorf("unexpected type for CustomResourceDefinition: %T", objs[0])
		}
		return crd.Spec.Names.Plural, true, nil
	}

	if pnkr.clientset != nil && pnkr.clientset.Discovery() != nil {
		_, resourceLists, err := pnkr.clientset.Discovery().ServerGroupsAndResources()
		if err != nil {
			if !discovery.IsGroupDiscoveryFailedError(err) {
				return "", false, fmt.Errorf("failed to discover server resources: %w", err)
			}
			var discoveryErr *discovery.ErrGroupDiscoveryFailed
			if errors.As(err, &discoveryErr) {
				for gv, groupErr := range discoveryErr.Groups {
					if gv.Group == group {
						return "", false, fmt.Errorf("failed to discover resources for group %q: %w", group, groupErr)
					}
				}
			}
		}
		for _, rl := range resourceLists {
			gv, err := schema.ParseGroupVersion(rl.GroupVersion)
			if err != nil {
				continue
			}
			if gv.Group != group {
				continue
			}
			for _, r := range rl.APIResources {
				if r.Kind == kind {
					return r.Name, true, nil
				}
			}
		}
	}

	return "", false, nil
}

func (pnkr *PodNetworkKindReconciler) setImplementationTypeReadyCondition(podNetworkKind *v1alpha1.PodNetworkKind, group, plural string, found, conflict bool) bool {
	status := metav1.ConditionTrue
	reason := v1alpha1.PodNetworkKindReasonCompliant
	message := ""

	switch {
	case !found:
		status = metav1.ConditionFalse
		reason = v1alpha1.PodNetworkKindReasonResourceTypeNotFound
		message = "Resource Type not found"
	case !pnkr.ruleWatcher.HasPermission("list", group, plural):
		status = metav1.ConditionFalse
		reason = v1alpha1.PodNetworkKindReasonMissingRBAC
		message = "Insufficient permissions to access the Resources"
	case conflict:
		status = metav1.ConditionFalse
		reason = v1alpha1.PodNetworkKindReasonTypeConflict
		message = "Another PodNetworkKind already references the same ImplementationType"
	}

	return meta.SetStatusCondition(&podNetworkKind.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.PodNetworkKindConditionImplementationTypeReady,
		Status:             status,
		ObservedGeneration: podNetworkKind.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (pnkr *PodNetworkKindReconciler) hasTypeConflict(podNetworkKind *v1alpha1.PodNetworkKind) (bool, error) {
	objs, err := pnkr.podNetworkKindIndexer.ByIndex(
		implementationTypePodNetworkKindIndex,
		podNetworkKind.Spec.ImplementationType.String(),
	)
	if err != nil {
		return false, fmt.Errorf("failed to get PodNetworkKinds by implementationType: %w", err)
	}

	for _, obj := range objs {
		other, ok := obj.(*v1alpha1.PodNetworkKind)
		if !ok {
			continue
		}
		if other.Name == podNetworkKind.Name {
			continue
		}

		if other.CreationTimestamp.Before(&podNetworkKind.CreationTimestamp) ||
			other.CreationTimestamp.Equal(&podNetworkKind.CreationTimestamp) {
			return true, nil
		}
	}

	return false, nil
}
