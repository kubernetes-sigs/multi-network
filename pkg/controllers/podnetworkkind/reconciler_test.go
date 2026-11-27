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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	authv1 "k8s.io/api/authorization/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsFake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
	podNetworkKindFake "sigs.k8s.io/multi-network-api/pkg/client/clientset/versioned/fake"
	podNetworkKindInformers "sigs.k8s.io/multi-network-api/pkg/client/informers/externalversions"
	"sigs.k8s.io/multi-network/pkg/ruleswatcher"
)

func newReconciler(
	ctx context.Context,
	t *testing.T,
	initialPodNetworkKindObjects []runtime.Object,
	initialCRDObjects []runtime.Object,
) (*fake.Clientset, *podNetworkKindFake.Clientset, *apiextensionsFake.Clientset, *ruleswatcher.Watcher, *PodNetworkKindReconciler) {
	fakeKubeClient := fake.NewSimpleClientset()
	fakeMultiNetworkClient := podNetworkKindFake.NewSimpleClientset(initialPodNetworkKindObjects...)
	fakeApiExtensionsClient := apiextensionsFake.NewSimpleClientset(initialCRDObjects...)

	networkKindInformerFactory := podNetworkKindInformers.NewSharedInformerFactory(fakeMultiNetworkClient, 0)
	apiextensionsInformerFactory := apiextensionsinformers.NewSharedInformerFactory(fakeApiExtensionsClient, 0)

	ruleWatcher := ruleswatcher.New(fakeKubeClient)

	crdIndexer := apiextensionsInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer()
	pnkIndexer := networkKindInformerFactory.Multinetwork().V1alpha1().PodNetworkKinds().Informer().GetIndexer()

	if err := AddCustomResourceDefinitionIndexers(crdIndexer); err != nil {
		t.Fatal("failed to add CRD indexers:", err)
	}
	if err := AddPodNetworkKindIndexers(pnkIndexer); err != nil {
		t.Fatal("failed to add PodNetworkKind indexers:", err)
	}

	reconciler := NewPodNetworkKindReconciler(
		crdIndexer,
		pnkIndexer,
		networkKindInformerFactory.Multinetwork().V1alpha1().PodNetworkKinds().Lister(),
		fakeMultiNetworkClient.MultinetworkV1alpha1().PodNetworkKinds(),
		ruleWatcher,
		fakeKubeClient,
	)

	networkKindInformerFactory.Start(ctx.Done())
	apiextensionsInformerFactory.Start(ctx.Done())
	networkKindInformerFactory.WaitForCacheSync(ctx.Done())
	apiextensionsInformerFactory.WaitForCacheSync(ctx.Done())

	return fakeKubeClient, fakeMultiNetworkClient, fakeApiExtensionsClient, ruleWatcher, reconciler
}

func TestPodNetworkKindReconciler_Reconcile(t *testing.T) {
	podNetworkKindObj := func() *v1alpha1.PodNetworkKind {
		return &v1alpha1.PodNetworkKind{
			ObjectMeta: metav1.ObjectMeta{Name: "example-com-mynetwork"},
			Spec: v1alpha1.PodNetworkKindSpec{
				ImplementationType: metav1.GroupKind{Group: "example.com", Kind: "MyNetwork"},
			},
		}
	}

	readyCRD := func() *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "mynetworks.example.com"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "example.com",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Kind:       "MyNetwork",
					Plural:     "mynetworks",
					Categories: v1alpha1.Categories,
				},
			},
			Status: apiextensionsv1.CustomResourceDefinitionStatus{
				Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
					{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
					{Type: apiextensionsv1.NamesAccepted, Status: apiextensionsv1.ConditionTrue},
				},
			},
		}
	}

	allowRules := func(fakeKubeClient *fake.Clientset) {
		fakeKubeClient.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, &authv1.SelfSubjectRulesReview{
				Status: authv1.SubjectRulesReviewStatus{
					ResourceRules: []authv1.ResourceRule{
						{
							Verbs:     []string{"list"},
							APIGroups: []string{"example.com"},
							Resources: []string{"mynetworks"},
						},
					},
				},
			}, nil
		})
	}

	tests := []struct {
		name                         string
		initialPodNetworkKindObjects []runtime.Object
		initialCRDObjects            []runtime.Object
		fakeKubeClientSetup          func(*fake.Clientset)
		fakeMultiNetworkClientSetup  func(*podNetworkKindFake.Clientset)
		customReconcilerSetup        func(*PodNetworkKindReconciler)
		podNetworkKindName           string
		wantErr                      bool
		expectedPodNetworkKind       *v1alpha1.PodNetworkKind
	}{
		{
			name:               "PodNetworkKind not found",
			podNetworkKindName: "nonexistent",
		},
		{
			name:                         "Resource Type not found",
			initialPodNetworkKindObjects: []runtime.Object{podNetworkKindObj()},
			podNetworkKindName:           "example-com-mynetwork",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionFalse,
					Reason:  v1alpha1.PodNetworkKindReasonResourceTypeNotFound,
					Message: "Resource Type not found",
				}}
				return nk
			}(),
		},
		{
			name: "native resource type found and compliant",
			initialPodNetworkKindObjects: []runtime.Object{
				&v1alpha1.PodNetworkKind{
					ObjectMeta: metav1.ObjectMeta{Name: "native-net"},
					Spec: v1alpha1.PodNetworkKindSpec{
						ImplementationType: metav1.GroupKind{Group: "", Kind: "Pod"},
					},
				},
			},
			fakeKubeClientSetup: func(fakeKubeClient *fake.Clientset) {
				fakeKubeClient.Resources = []*metav1.APIResourceList{
					{
						GroupVersion: "v1",
						APIResources: []metav1.APIResource{
							{
								Name: "pods",
								Kind: "Pod",
							},
						},
					},
				}
				fakeKubeClient.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, &authv1.SelfSubjectRulesReview{
						Status: authv1.SubjectRulesReviewStatus{
							ResourceRules: []authv1.ResourceRule{
								{
									Verbs:     []string{"list"},
									APIGroups: []string{""},
									Resources: []string{"pods"},
								},
							},
						},
					}, nil
				})
			},
			podNetworkKindName: "native-net",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := &v1alpha1.PodNetworkKind{
					ObjectMeta: metav1.ObjectMeta{Name: "native-net"},
					Spec: v1alpha1.PodNetworkKindSpec{
						ImplementationType: metav1.GroupKind{Group: "", Kind: "Pod"},
					},
				}
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  v1alpha1.PodNetworkKindReasonCompliant,
					Message: "",
				}}
				return nk
			}(),
		},
		{
			name:                         "missing RBAC",
			initialPodNetworkKindObjects: []runtime.Object{podNetworkKindObj()},
			initialCRDObjects:            []runtime.Object{readyCRD()},
			podNetworkKindName:           "example-com-mynetwork",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionFalse,
					Reason:  v1alpha1.PodNetworkKindReasonMissingRBAC,
					Message: "Insufficient permissions to access the Resources",
				}}
				return nk
			}(),
		},
		{
			name:                         "compliant",
			initialPodNetworkKindObjects: []runtime.Object{podNetworkKindObj()},
			initialCRDObjects:            []runtime.Object{readyCRD()},
			fakeKubeClientSetup:          allowRules,
			podNetworkKindName:           "example-com-mynetwork",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  v1alpha1.PodNetworkKindReasonCompliant,
					Message: "",
				}}
				return nk
			}(),
		},
		{
			name: "type conflict on newer PodNetworkKind",
			initialPodNetworkKindObjects: []runtime.Object{
				func() *v1alpha1.PodNetworkKind {
					nk := podNetworkKindObj()
					nk.Name = "older-pnk"
					nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
					return nk
				}(),
				func() *v1alpha1.PodNetworkKind {
					nk := podNetworkKindObj()
					nk.Name = "newer-pnk"
					nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
					return nk
				}(),
			},
			initialCRDObjects:   []runtime.Object{readyCRD()},
			fakeKubeClientSetup: allowRules,
			podNetworkKindName:  "newer-pnk",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Name = "newer-pnk"
				nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionFalse,
					Reason:  v1alpha1.PodNetworkKindReasonTypeConflict,
					Message: "Another PodNetworkKind already references the same ImplementationType",
				}}
				return nk
			}(),
		},
		{
			name: "strictly older PodNetworkKind remains compliant when newer conflict exists",
			initialPodNetworkKindObjects: []runtime.Object{
				func() *v1alpha1.PodNetworkKind {
					nk := podNetworkKindObj()
					nk.Name = "older-pnk"
					nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
					return nk
				}(),
				func() *v1alpha1.PodNetworkKind {
					nk := podNetworkKindObj()
					nk.Name = "newer-pnk"
					nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
					return nk
				}(),
			},
			initialCRDObjects:   []runtime.Object{readyCRD()},
			fakeKubeClientSetup: allowRules,
			podNetworkKindName:  "older-pnk",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Name = "older-pnk"
				nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  v1alpha1.PodNetworkKindReasonCompliant,
					Message: "",
				}}
				return nk
			}(),
		},
		{
			name: "type conflict on both PodNetworkKinds with identical creationTimestamp",
			initialPodNetworkKindObjects: []runtime.Object{
				func() *v1alpha1.PodNetworkKind {
					nk := podNetworkKindObj()
					nk.Name = "pnk-a"
					nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
					return nk
				}(),
				func() *v1alpha1.PodNetworkKind {
					nk := podNetworkKindObj()
					nk.Name = "pnk-b"
					nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
					return nk
				}(),
			},
			initialCRDObjects:   []runtime.Object{readyCRD()},
			fakeKubeClientSetup: allowRules,
			podNetworkKindName:  "pnk-a",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Name = "pnk-a"
				nk.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
				nk.Status.Conditions = []metav1.Condition{{
					Type:    v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:  metav1.ConditionFalse,
					Reason:  v1alpha1.PodNetworkKindReasonTypeConflict,
					Message: "Another PodNetworkKind already references the same ImplementationType",
				}}
				return nk
			}(),
		},
		{
			name: "status has not changed",
			initialPodNetworkKindObjects: []runtime.Object{func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Status.Conditions = []metav1.Condition{{
					Type:               v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             v1alpha1.PodNetworkKindReasonCompliant,
					Message:            "",
					ObservedGeneration: nk.Generation,
				}}
				return nk
			}()},
			initialCRDObjects:   []runtime.Object{readyCRD()},
			fakeKubeClientSetup: allowRules,
			fakeMultiNetworkClientSetup: func(fakeMultiNetworkClient *podNetworkKindFake.Clientset) {
				fakeMultiNetworkClient.PrependReactor("update", "podnetworkkinds", func(action k8stesting.Action) (bool, runtime.Object, error) {
					t.Errorf("UpdateStatus should not have been called when status did not change")
					return true, nil, nil
				})
			},
			podNetworkKindName: "example-com-mynetwork",
			expectedPodNetworkKind: func() *v1alpha1.PodNetworkKind {
				nk := podNetworkKindObj()
				nk.Status.Conditions = []metav1.Condition{{
					Type:               v1alpha1.PodNetworkKindConditionImplementationTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             v1alpha1.PodNetworkKindReasonCompliant,
					Message:            "",
					ObservedGeneration: nk.Generation,
				}}
				return nk
			}(),
		},
		{
			name:                         "failed status update",
			initialPodNetworkKindObjects: []runtime.Object{podNetworkKindObj()},
			initialCRDObjects:            []runtime.Object{readyCRD()},
			fakeKubeClientSetup:          allowRules,
			fakeMultiNetworkClientSetup: func(fakeMultiNetworkClient *podNetworkKindFake.Clientset) {
				fakeMultiNetworkClient.PrependReactor("update", "podnetworkkinds", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("update failed")
				})
			},
			podNetworkKindName: "example-com-mynetwork",
			wantErr:            true,
		},
		{
			name:                         "unexpected object type in CRD indexer",
			initialPodNetworkKindObjects: []runtime.Object{podNetworkKindObj()},
			customReconcilerSetup: func(nkr *PodNetworkKindReconciler) {
				nkr.customResourceDefinitionIndexer = cache.NewIndexer(
					func(obj interface{}) (string, error) { return "test-key", nil },
					cache.Indexers{
						groupKindCustomResourceDefinitionIndex: func(obj interface{}) ([]string, error) {
							return []string{schema.GroupKind{Group: "example.com", Kind: "MyNetwork"}.String()}, nil
						},
					},
				)
				_ = nkr.customResourceDefinitionIndexer.Add(&v1alpha1.PodNetworkKind{
					ObjectMeta: metav1.ObjectMeta{Name: "unexpected-object"},
				})
			},
			podNetworkKindName: "example-com-mynetwork",
			wantErr:            true,
		},
		{
			name:                         "discovery error returns error",
			initialPodNetworkKindObjects: []runtime.Object{podNetworkKindObj()},
			fakeKubeClientSetup: func(fakeKubeClient *fake.Clientset) {
				fakeKubeClient.PrependReactor("get", "resource", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("discovery connection refused")
				})
			},
			podNetworkKindName: "example-com-mynetwork",
			wantErr:            true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fakeKubeClient, fakeMultiNetworkClient, _, ruleWatcher, nkr := newReconciler(ctx, t, tt.initialPodNetworkKindObjects, tt.initialCRDObjects)

			if tt.fakeKubeClientSetup != nil {
				tt.fakeKubeClientSetup(fakeKubeClient)
			}
			if tt.fakeMultiNetworkClientSetup != nil {
				tt.fakeMultiNetworkClientSetup(fakeMultiNetworkClient)
			}
			if tt.customReconcilerSetup != nil {
				tt.customReconcilerSetup(nkr)
			}

			_ = ruleWatcher.Sync(ctx)

			err := nkr.Reconcile(ctx, tt.podNetworkKindName)
			if (err != nil) != tt.wantErr {
				t.Errorf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.expectedPodNetworkKind != nil {
				nk, getErr := fakeMultiNetworkClient.MultinetworkV1alpha1().PodNetworkKinds().Get(ctx, tt.podNetworkKindName, metav1.GetOptions{})
				if getErr != nil {
					t.Fatalf("failed to get PodNetworkKind: %v", getErr)
				}
				if diff := cmp.Diff(tt.expectedPodNetworkKind, nk,
					cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				); diff != "" {
					t.Errorf("PodNetworkKind mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestNewPodNetworkKindReconciler(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		fakeKubeClient := fake.NewSimpleClientset()
		fakeMultiNetworkClient := podNetworkKindFake.NewSimpleClientset()
		fakeApiExtensionsClient := apiextensionsFake.NewSimpleClientset()

		podNetworkKindInformerFactory := podNetworkKindInformers.NewSharedInformerFactory(fakeMultiNetworkClient, 0)
		apiextensionsInformerFactory := apiextensionsinformers.NewSharedInformerFactory(fakeApiExtensionsClient, 0)

		ruleWatcher := ruleswatcher.New(fakeKubeClient)
		reconciler := NewPodNetworkKindReconciler(
			apiextensionsInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),
			podNetworkKindInformerFactory.Multinetwork().V1alpha1().PodNetworkKinds().Informer().GetIndexer(),
			podNetworkKindInformerFactory.Multinetwork().V1alpha1().PodNetworkKinds().Lister(),
			fakeMultiNetworkClient.MultinetworkV1alpha1().PodNetworkKinds(),
			ruleWatcher,
			fakeKubeClient,
		)
		if reconciler == nil {
			t.Fatal("expected reconciler to be non-nil")
		}
	})

	t.Run("indexer function handles CRD and non-CRD objects", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		_, _, _, _, reconciler := newReconciler(ctx, t, nil, nil)
		indexFunc := reconciler.customResourceDefinitionIndexer.GetIndexers()[groupKindCustomResourceDefinitionIndex]
		if indexFunc == nil {
			t.Fatal("expected groupKind index function to be registered")
		}

		// Non-CRD object
		keys, err := indexFunc(&v1alpha1.PodNetworkKind{})
		if err != nil {
			t.Errorf("unexpected error for non-CRD: %v", err)
		}
		if keys != nil {
			t.Errorf("expected nil keys for non-CRD, got %v", keys)
		}

		// Valid CRD
		crd := &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "example.com",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Kind: "MyNetwork",
				},
			},
		}
		keys, err = indexFunc(crd)
		if err != nil {
			t.Errorf("unexpected error for CRD: %v", err)
		}
		expectedKey := schema.GroupKind{Group: "example.com", Kind: "MyNetwork"}.String()
		if len(keys) != 1 || keys[0] != expectedKey {
			t.Errorf("expected keys [%q], got %v", expectedKey, keys)
		}
	})
}
