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
	"fmt"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	v1apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
	v1alpha1client "sigs.k8s.io/multi-network-api/pkg/client/clientset/versioned/typed/apis/v1alpha1"
	v1alpha1networkkindinformers "sigs.k8s.io/multi-network-api/pkg/client/informers/externalversions/apis/v1alpha1"
	v1alpha1networkkindlisters "sigs.k8s.io/multi-network-api/pkg/client/listers/apis/v1alpha1"
	"sigs.k8s.io/multi-network/pkg/ruleswatcher"
)

const (
	queueName = "pod_network_kind"
)

type reconciler interface {
	Reconcile(ctx context.Context, podNetworkKindName string) error
}

// PodNetworkKindController manages the reconciliation of PodNetworkKind resources.
type PodNetworkKindController struct {
	customResourceDefinitionSynced cache.InformerSynced
	podNetworkKindSynced           cache.InformerSynced
	ruleWatcherSynced              cache.InformerSynced

	podNetworkKindIndexer cache.Indexer
	podNetworkKindLister  v1alpha1networkkindlisters.PodNetworkKindLister

	podNetworkKindQueue workqueue.TypedRateLimitingInterface[string]

	podNetworkKindReconciler reconciler
}

// NewPodNetworkKindController creates a new PodNetworkKindController and sets up event handlers.
func NewPodNetworkKindController(
	podNetworkKindInformer v1alpha1networkkindinformers.PodNetworkKindInformer,
	customResourceDefinitionInformer v1apiextensionsinformers.CustomResourceDefinitionInformer,
	networkKindClient v1alpha1client.PodNetworkKindInterface,
	ruleWatcher *ruleswatcher.Watcher,
	client clientset.Interface,
) (*PodNetworkKindController, error) {

	nkc := &PodNetworkKindController{
		customResourceDefinitionSynced: customResourceDefinitionInformer.Informer().HasSynced,
		podNetworkKindSynced:           podNetworkKindInformer.Informer().HasSynced,
		ruleWatcherSynced:              ruleWatcher.HasSynced,
		podNetworkKindIndexer:          podNetworkKindInformer.Informer().GetIndexer(),
		podNetworkKindLister:           podNetworkKindInformer.Lister(),
		podNetworkKindQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: queueName},
		),
	}

	err := AddCustomResourceDefinitionIndexers(customResourceDefinitionInformer.Informer().GetIndexer())
	if err != nil {
		return nil, fmt.Errorf("failed to add indexers for CustomResourceDefinition: %w", err)
	}

	err = AddPodNetworkKindIndexers(nkc.podNetworkKindIndexer)
	if err != nil {
		return nil, fmt.Errorf("failed to add indexers for PodNetworkKind: %w", err)
	}

	nkc.podNetworkKindReconciler = NewPodNetworkKindReconciler(
		customResourceDefinitionInformer.Informer().GetIndexer(),
		nkc.podNetworkKindIndexer,
		nkc.podNetworkKindLister,
		networkKindClient,
		ruleWatcher,
		client,
	)

	if _, err := podNetworkKindInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nkc.onPodNetworkKindEvent,
		UpdateFunc: func(oldObj, newObj interface{}) { nkc.onPodNetworkKindEvent(newObj) },
		DeleteFunc: nkc.onPodNetworkKindEvent,
	}); err != nil {
		return nil, err
	}

	if _, err := customResourceDefinitionInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { nkc.onCustomResourceDefinitionEvent(nil, obj) },
		UpdateFunc: func(oldObj, newObj interface{}) { nkc.onCustomResourceDefinitionEvent(oldObj, newObj) },
		DeleteFunc: func(obj interface{}) { nkc.onCustomResourceDefinitionEvent(obj, nil) },
	}); err != nil {
		return nil, err
	}

	ruleWatcher.AddEventHandler(func(oldRules, newRules []authv1.ResourceRule) {
		nkc.enqueueAllPodNetworkKinds()
	})

	return nkc, nil
}

func (pnkc *PodNetworkKindController) onPodNetworkKindEvent(obj interface{}) {
	networkKind, ok := extractPodNetworkKind(obj)
	if !ok {
		return
	}

	pnkc.podNetworkKindQueue.Add(networkKind.Name)
	pnkc.enqueuePodNetworkKindsForGroupKind(networkKind.Spec.ImplementationType.Group, networkKind.Spec.ImplementationType.Kind)
}

func (pnkc *PodNetworkKindController) enqueueAllPodNetworkKinds() {
	networkKinds, err := pnkc.podNetworkKindLister.List(labels.Everything())
	if err != nil {
		klog.Error(err, "Failed to list PodNetworkKinds")
		return
	}

	for _, nk := range networkKinds {
		pnkc.podNetworkKindQueue.Add(nk.Name)
	}
}

func extractPodNetworkKind(obj interface{}) (*v1alpha1.PodNetworkKind, bool) {
	switch t := obj.(type) {
	case *v1alpha1.PodNetworkKind:
		return t, true
	case cache.DeletedFinalStateUnknown:
		pnk, ok := t.Obj.(*v1alpha1.PodNetworkKind)
		if !ok {
			klog.Error(nil, "Expected PodNetworkKind in tombstone", "actual", fmt.Sprintf("%T", t.Obj))
			return nil, false
		}
		return pnk, true
	default:
		klog.Error(nil, "Expected PodNetworkKind", "actual", fmt.Sprintf("%T", obj))
		return nil, false
	}
}

func (pnkc *PodNetworkKindController) onCustomResourceDefinitionEvent(oldObj, newObj interface{}) {
	for _, obj := range []interface{}{oldObj, newObj} {
		if obj == nil {
			continue
		}

		crd, ok := extractCRD(obj)
		if !ok {
			continue
		}

		pnkc.enqueuePodNetworkKindsForGroupKind(crd.Spec.Group, crd.Spec.Names.Kind)
	}
}

func extractCRD(obj interface{}) (*apiextensionsv1.CustomResourceDefinition, bool) {
	switch t := obj.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		return t, true
	case cache.DeletedFinalStateUnknown:
		crd, ok := t.Obj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			klog.Error(nil, "Expected CustomResourceDefinition in tombstone", "actual", fmt.Sprintf("%T", t.Obj))
			return nil, false
		}
		return crd, true
	default:
		klog.Error(nil, "Expected CustomResourceDefinition", "actual", fmt.Sprintf("%T", obj))
		return nil, false
	}
}

func (pnkc *PodNetworkKindController) enqueuePodNetworkKindsForGroupKind(group, kind string) {
	objs, err := pnkc.podNetworkKindIndexer.ByIndex(
		implementationTypePodNetworkKindIndex,
		schema.GroupKind{Group: group, Kind: kind}.String(),
	)
	if err != nil {
		klog.Error(err, "Failed to get PodNetworkKinds by implementationType", "group", group, "kind", kind)
		return
	}

	for _, obj := range objs {
		if nk, ok := obj.(*v1alpha1.PodNetworkKind); ok {
			pnkc.podNetworkKindQueue.Add(nk.Name)
		}
	}
}

// Run starts the controller workers after waiting for informer caches to sync.
func (pnkc *PodNetworkKindController) Run(ctx context.Context, workers int) error {
	if !cache.WaitForNamedCacheSyncWithContext(ctx, pnkc.podNetworkKindSynced, pnkc.customResourceDefinitionSynced, pnkc.ruleWatcherSynced) {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	var wg sync.WaitGroup
	defer func() {
		klog.FromContext(ctx).Info("Shutting down network kind controller")
		pnkc.podNetworkKindQueue.ShutDown()
		wg.Wait()
	}()

	for i := 0; i < workers; i++ {
		wg.Go(func() {
			wait.UntilWithContext(ctx, pnkc.runWorker, time.Second)
		})
	}

	<-ctx.Done()

	return nil
}

func (pnkc *PodNetworkKindController) runWorker(ctx context.Context) {
	for pnkc.processNextWorkItem(ctx) {
	}
}

func (pnkc *PodNetworkKindController) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := pnkc.podNetworkKindQueue.Get()
	if shutdown {
		return false
	}
	defer pnkc.podNetworkKindQueue.Done(key)

	err := pnkc.podNetworkKindReconciler.Reconcile(ctx, key)
	if err == nil {
		pnkc.podNetworkKindQueue.Forget(key)
		return true
	}

	runtime.HandleErrorWithContext(ctx, err, "Work item failed", "item", key)
	pnkc.podNetworkKindQueue.AddRateLimited(key)

	return true
}
