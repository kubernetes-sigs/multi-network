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

package ruleswatcher

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func setupFakeClientWithRules(rules []authv1.ResourceRule, incomplete bool, evalErr string) *fake.Clientset {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		review, ok := createAction.GetObject().(*authv1.SelfSubjectRulesReview)
		if !ok {
			return false, nil, nil
		}

		res := review.DeepCopy()
		res.Status = authv1.SubjectRulesReviewStatus{
			ResourceRules:   rules,
			Incomplete:      incomplete,
			EvaluationError: evalErr,
		}
		return true, res, nil
	})
	return client
}

func TestNew(t *testing.T) {
	client := fake.NewSimpleClientset()

	t.Run("default options", func(t *testing.T) {
		w := New(client)
		if w.client != client {
			t.Errorf("expected client %v, got %v", client, w.client)
		}
		if w.resyncPeriod != DefaultResyncPeriod {
			t.Errorf("expected resyncPeriod %v, got %v", DefaultResyncPeriod, w.resyncPeriod)
		}
		if w.namespace != AllNamespaces {
			t.Errorf("expected default namespace %q, got %q", AllNamespaces, w.namespace)
		}
	})

	t.Run("custom options", func(t *testing.T) {
		w := New(
			client,
			WithNamespace("test-namespace"),
			WithResyncPeriod(5*time.Second),
		)
		if w.namespace != "test-namespace" {
			t.Errorf("expected namespace %q, got %q", "test-namespace", w.namespace)
		}
		if w.resyncPeriod != 5*time.Second {
			t.Errorf("expected resyncPeriod 5s, got %v", w.resyncPeriod)
		}
	})

	t.Run("empty namespace defaults to AllNamespaces", func(t *testing.T) {
		w := New(client, WithNamespace(""))
		if w.namespace != AllNamespaces {
			t.Errorf("expected namespace %q, got %q", AllNamespaces, w.namespace)
		}
	})

	t.Run("non-positive resync period defaults to DefaultResyncPeriod", func(t *testing.T) {
		w := New(client, WithResyncPeriod(-1*time.Second))
		if w.resyncPeriod != DefaultResyncPeriod {
			t.Errorf("expected resyncPeriod %v, got %v", DefaultResyncPeriod, w.resyncPeriod)
		}
	})
}

func TestSync(t *testing.T) {
	rules := []authv1.ResourceRule{
		{
			Verbs:     []string{"get", "list"},
			APIGroups: []string{"example.com"},
			Resources: []string{"mynetworks"},
		},
	}

	t.Run("successful sync", func(t *testing.T) {
		client := setupFakeClientWithRules(rules, false, "")
		w := New(client)

		var handlerCalled atomic.Bool
		var receivedOld, receivedNew []authv1.ResourceRule
		var mu sync.Mutex
		w.AddEventHandler(func(oldRules, newRules []authv1.ResourceRule) {
			handlerCalled.Store(true)
			mu.Lock()
			receivedOld = oldRules
			receivedNew = newRules
			mu.Unlock()
		})

		ctx := t.Context()
		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !w.HasSynced() {
			t.Error("expected HasSynced to be true")
		}
		if !handlerCalled.Load() {
			t.Error("expected event handler to be called on first sync")
		}
		mu.Lock()
		if len(receivedOld) != 0 {
			t.Errorf("expected empty old rules, got %v", receivedOld)
		}
		if diff := cmp.Diff(rules, receivedNew); diff != "" {
			t.Errorf("new rules mismatch (-want +got):\n%s", diff)
		}
		mu.Unlock()

		// Second sync with identical rules should not trigger handler
		handlerCalled.Store(false)
		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error on second sync: %v", err)
		}
		if handlerCalled.Load() {
			t.Error("handler should not be called when rules have not changed")
		}
	})

	t.Run("sync with rule change triggers handler", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		var currentRules []authv1.ResourceRule
		var rMu sync.Mutex
		client.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			rMu.Lock()
			defer rMu.Unlock()
			return true, &authv1.SelfSubjectRulesReview{
				Status: authv1.SubjectRulesReviewStatus{
					ResourceRules: currentRules,
				},
			}, nil
		})

		w := New(client)
		var callCount atomic.Int32
		w.AddEventHandler(func(oldRules, newRules []authv1.ResourceRule) {
			callCount.Add(1)
		})

		ctx := t.Context()
		rMu.Lock()
		currentRules = rules
		rMu.Unlock()

		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if callCount.Load() != 1 {
			t.Fatalf("expected 1 call, got %d", callCount.Load())
		}

		// Update rules
		rMu.Lock()
		currentRules = append(currentRules, authv1.ResourceRule{
			Verbs:     []string{"create"},
			APIGroups: []string{"example.com"},
			Resources: []string{"mynetworks"},
		})
		rMu.Unlock()

		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if callCount.Load() != 2 {
			t.Fatalf("expected 2 calls, got %d", callCount.Load())
		}
	})

	t.Run("incomplete rules with evaluation error logged", func(t *testing.T) {
		client := setupFakeClientWithRules(rules, true, "authorizer error")
		w := New(client)

		ctx := t.Context()
		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !w.HasSynced() {
			t.Error("expected HasSynced to be true")
		}
	})

	t.Run("API error does not overwrite cached rules", func(t *testing.T) {
		client := setupFakeClientWithRules(rules, false, "")
		w := New(client)

		ctx := t.Context()
		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Inject failure
		client.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("server unavailable")
		})

		err := w.Sync(ctx)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		// Cached rules should be preserved
		cached := w.GetRules()
		if diff := cmp.Diff(rules, cached); diff != "" {
			t.Errorf("cached rules mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestRun(t *testing.T) {
	rules := []authv1.ResourceRule{
		{
			Verbs:     []string{"list"},
			APIGroups: []string{"multinetwork.networking.x-k8s.io"},
			Resources: []string{"podnetworkkinds"},
		},
	}

	t.Run("successful periodic sync and cancel", func(t *testing.T) {
		client := setupFakeClientWithRules(rules, false, "")
		w := New(client, WithResyncPeriod(20*time.Millisecond))

		ctx, cancel := context.WithCancel(t.Context())
		runErrCh := make(chan error, 1)
		go func() {
			runErrCh <- w.Run(ctx)
		}()

		if !w.WaitForSync(ctx) {
			t.Fatal("timed out waiting for initial sync")
		}

		if !w.HasPermission("list", "multinetwork.networking.x-k8s.io", "podnetworkkinds") {
			t.Error("expected permission to be granted")
		}

		cancel()

		select {
		case err := <-runErrCh:
			if err != nil {
				t.Fatalf("unexpected error from Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Run to exit")
		}
	})

	t.Run("handles sync errors gracefully", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("temporary sync failure")
		})

		w := New(client, WithResyncPeriod(10*time.Millisecond))

		ctx, cancel := context.WithCancel(t.Context())
		runErrCh := make(chan error, 1)
		go func() {
			runErrCh <- w.Run(ctx)
		}()

		time.Sleep(50 * time.Millisecond)
		cancel()

		select {
		case err := <-runErrCh:
			if err != nil {
				t.Fatalf("unexpected error from Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Run to exit")
		}
	})
}

func TestWaitForSync(t *testing.T) {
	client := setupFakeClientWithRules(nil, false, "")
	w := New(client)

	t.Run("canceled context", func(t *testing.T) {
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()

		if w.WaitForSync(canceledCtx) {
			t.Error("expected WaitForSync to return false on canceled context")
		}
	})

	t.Run("successful sync", func(t *testing.T) {
		ctx := t.Context()
		if err := w.Sync(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !w.WaitForSync(ctx) {
			t.Error("expected WaitForSync to return true after sync")
		}
	})
}

func TestGetRules(t *testing.T) {
	original := []authv1.ResourceRule{
		{
			Verbs:     []string{"get"},
			APIGroups: []string{""},
			Resources: []string{"pods"},
		},
	}
	client := setupFakeClientWithRules(original, false, "")
	w := New(client)

	ctx := t.Context()
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	retrieved := w.GetRules()
	retrieved[0].Verbs[0] = "mutated"

	// Original should not be modified
	fresh := w.GetRules()
	if fresh[0].Verbs[0] != "get" {
		t.Errorf("GetRules did not return a deep copy, got: %v", fresh[0].Verbs)
	}
}

func TestHasPermission(t *testing.T) {
	rules := []authv1.ResourceRule{
		{
			Verbs:     []string{"get", "list", "watch"},
			APIGroups: []string{"example.com"},
			Resources: []string{"mynetworks"},
		},
		{
			Verbs:     []string{"*"},
			APIGroups: []string{""},
			Resources: []string{"services"},
		},
		{
			Verbs:     []string{"get"},
			APIGroups: []string{"admin.example.com"},
			Resources: []string{"*"},
		},
		{
			Verbs:         []string{"get"},
			APIGroups:     []string{"named.example.com"},
			Resources:     []string{"configs"},
			ResourceNames: []string{"allowed-config"},
		},
		{
			Verbs:     []string{"update"},
			APIGroups: []string{"multinetwork.networking.x-k8s.io"},
			Resources: []string{"podnetworkkinds/status"},
		},
	}

	client := setupFakeClientWithRules(rules, false, "")
	w := New(client)
	if err := w.Sync(t.Context()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name         string
		verb         string
		apiGroup     string
		resource     string
		resourceName string
		wantAllowed  bool
	}{
		{
			name:        "exact match",
			verb:        "list",
			apiGroup:    "example.com",
			resource:    "mynetworks",
			wantAllowed: true,
		},
		{
			name:        "unmatched verb",
			verb:        "delete",
			apiGroup:    "example.com",
			resource:    "mynetworks",
			wantAllowed: false,
		},
		{
			name:        "unmatched apiGroup",
			verb:        "get",
			apiGroup:    "wrong.com",
			resource:    "mynetworks",
			wantAllowed: false,
		},
		{
			name:        "unmatched resource",
			verb:        "get",
			apiGroup:    "example.com",
			resource:    "other",
			wantAllowed: false,
		},
		{
			name:        "wildcard verb match on core group",
			verb:        "delete",
			apiGroup:    "",
			resource:    "services",
			wantAllowed: true,
		},
		{
			name:        "wildcard resource match",
			verb:        "get",
			apiGroup:    "admin.example.com",
			resource:    "any-resource",
			wantAllowed: true,
		},
		{
			name:        "subresource match",
			verb:        "update",
			apiGroup:    "multinetwork.networking.x-k8s.io",
			resource:    "podnetworkkinds/status",
			wantAllowed: true,
		},
		{
			name:         "matching resource name",
			verb:         "get",
			apiGroup:     "named.example.com",
			resource:     "configs",
			resourceName: "allowed-config",
			wantAllowed:  true,
		},
		{
			name:         "resource name allowed when rule has no resourceName restrictions",
			verb:         "get",
			apiGroup:     "example.com",
			resource:     "mynetworks",
			resourceName: "any-network-instance",
			wantAllowed:  true,
		},
		{
			name:         "non-matching resource name",
			verb:         "get",
			apiGroup:     "named.example.com",
			resource:     "configs",
			resourceName: "other-config",
			wantAllowed:  false,
		},
		{
			name:         "collection access denied when rule restricts resourceNames",
			verb:         "get",
			apiGroup:     "named.example.com",
			resource:     "configs",
			resourceName: "",
			wantAllowed:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if tt.resourceName != "" {
				got = w.HasResourcePermission(tt.verb, tt.apiGroup, tt.resource, tt.resourceName)
			} else {
				got = w.HasPermission(tt.verb, tt.apiGroup, tt.resource)
			}
			if got != tt.wantAllowed {
				t.Errorf("permission check got %v, want %v", got, tt.wantAllowed)
			}
		})
	}
}

func TestRulesEqual(t *testing.T) {
	rule1 := authv1.ResourceRule{
		Verbs:     []string{"get", "list"},
		APIGroups: []string{"example.com"},
		Resources: []string{"res-a", "res-b"},
	}
	rule1Permuted := authv1.ResourceRule{
		Verbs:     []string{"list", "get"},
		APIGroups: []string{"example.com"},
		Resources: []string{"res-b", "res-a"},
	}
	rule2 := authv1.ResourceRule{
		Verbs:     []string{"create"},
		APIGroups: []string{"example.com"},
		Resources: []string{"res-c"},
	}

	tests := []struct {
		name      string
		r1        []authv1.ResourceRule
		r2        []authv1.ResourceRule
		wantEqual bool
	}{
		{
			name:      "both empty",
			r1:        []authv1.ResourceRule{},
			r2:        []authv1.ResourceRule{},
			wantEqual: true,
		},
		{
			name:      "both nil",
			r1:        nil,
			r2:        nil,
			wantEqual: true,
		},
		{
			name:      "different lengths",
			r1:        []authv1.ResourceRule{rule1},
			r2:        []authv1.ResourceRule{rule1, rule2},
			wantEqual: false,
		},
		{
			name:      "identical single rule",
			r1:        []authv1.ResourceRule{rule1},
			r2:        []authv1.ResourceRule{rule1},
			wantEqual: true,
		},
		{
			name:      "permuted slice elements within rule",
			r1:        []authv1.ResourceRule{rule1},
			r2:        []authv1.ResourceRule{rule1Permuted},
			wantEqual: true,
		},
		{
			name:      "permuted rule order",
			r1:        []authv1.ResourceRule{rule1, rule2},
			r2:        []authv1.ResourceRule{rule2, rule1Permuted},
			wantEqual: true,
		},
		{
			name: "different verbs",
			r1:   []authv1.ResourceRule{rule1},
			r2: []authv1.ResourceRule{{
				Verbs:     []string{"delete"},
				APIGroups: []string{"example.com"},
				Resources: []string{"res-a", "res-b"},
			}},
			wantEqual: false,
		},
		{
			name: "different API groups",
			r1:   []authv1.ResourceRule{rule1},
			r2: []authv1.ResourceRule{{
				Verbs:     []string{"get", "list"},
				APIGroups: []string{"other.com"},
				Resources: []string{"res-a", "res-b"},
			}},
			wantEqual: false,
		},
		{
			name: "different resources",
			r1:   []authv1.ResourceRule{rule1},
			r2: []authv1.ResourceRule{{
				Verbs:     []string{"get", "list"},
				APIGroups: []string{"example.com"},
				Resources: []string{"different"},
			}},
			wantEqual: false,
		},
		{
			name: "different resource names",
			r1: []authv1.ResourceRule{{
				Verbs:         []string{"get"},
				APIGroups:     []string{""},
				Resources:     []string{"pods"},
				ResourceNames: []string{"pod-1"},
			}},
			r2: []authv1.ResourceRule{{
				Verbs:         []string{"get"},
				APIGroups:     []string{""},
				Resources:     []string{"pods"},
				ResourceNames: []string{"pod-2"},
			}},
			wantEqual: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rulesEqual(tt.r1, tt.r2)
			if got != tt.wantEqual {
				t.Errorf("rulesEqual() = %v, want %v", got, tt.wantEqual)
			}
		})
	}
}
