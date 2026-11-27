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
	"slices"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	// DefaultResyncPeriod is the default period between rules refreshes.
	DefaultResyncPeriod = 10 * time.Second
	// AllNamespaces is the wildcard namespace to evaluate rules across all namespaces.
	AllNamespaces = "*"
)

// RuleChangeHandler is called when the set of resource rules has changed.
type RuleChangeHandler func(oldRules, newRules []authv1.ResourceRule)

// Watcher periodically evaluates and caches the RBAC rules for the client.
type Watcher struct {
	client       clientset.Interface
	namespace    string
	resyncPeriod time.Duration

	mu        sync.RWMutex
	rules     []authv1.ResourceRule
	hasSynced bool
	handlers  []RuleChangeHandler
}

// Option allows configuring the Watcher.
type Option func(*Watcher)

// WithNamespace sets the namespace for the SelfSubjectRulesReview. Empty string or "*" queries rules across all namespaces.
func WithNamespace(namespace string) Option {
	return func(w *Watcher) {
		w.namespace = namespace
	}
}

// WithResyncPeriod sets the periodic interval between rules refreshes.
func WithResyncPeriod(period time.Duration) Option {
	return func(w *Watcher) {
		w.resyncPeriod = period
	}
}

// New creates a new rules Watcher.
func New(client clientset.Interface, opts ...Option) *Watcher {
	w := &Watcher{
		client:       client,
		namespace:    AllNamespaces,
		resyncPeriod: DefaultResyncPeriod,
	}
	for _, opt := range opts {
		opt(w)
	}
	if w.namespace == "" {
		w.namespace = AllNamespaces
	}
	if w.resyncPeriod <= 0 {
		w.resyncPeriod = DefaultResyncPeriod
	}
	return w
}

// AddEventHandler registers a handler that is called when rules change.
func (w *Watcher) AddEventHandler(handler RuleChangeHandler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers = append(w.handlers, handler)
}

// HasSynced returns true if the rules have been successfully retrieved at least once.
func (w *Watcher) HasSynced() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.hasSynced
}

// WaitForSync blocks until the rules have synced or the context is cancelled.
func (w *Watcher) WaitForSync(ctx context.Context) bool {
	return wait.PollUntilContextCancel(ctx, 10*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		return w.HasSynced(), nil
	}) == nil
}

// GetRules returns a copy of the current cached resource rules.
func (w *Watcher) GetRules() []authv1.ResourceRule {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return copyRules(w.rules)
}

// HasPermission checks if the client has permission for the specified verb, API group, and resource.
func (w *Watcher) HasPermission(verb, apiGroup, resource string) bool {
	return w.HasResourcePermission(verb, apiGroup, resource, "")
}

// HasResourcePermission checks if the client has permission for the specified verb, API group, resource, and resource name.
func (w *Watcher) HasResourcePermission(verb, apiGroup, resource, resourceName string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, rule := range w.rules {
		if ruleAllows(rule, verb, apiGroup, resource, resourceName) {
			return true
		}
	}
	return false
}

// Sync performs an immediate review of the subject rules via the Kubernetes API.
func (w *Watcher) Sync(ctx context.Context) error {
	review, err := w.client.AuthorizationV1().SelfSubjectRulesReviews().Create(
		ctx,
		&authv1.SelfSubjectRulesReview{
			Spec: authv1.SelfSubjectRulesReviewSpec{
				Namespace: w.namespace,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to create SelfSubjectRulesReview: %w", err)
	}

	if review.Status.Incomplete {
		klog.FromContext(ctx).V(2).Info("SelfSubjectRulesReview returned incomplete rules", "evaluationError", review.Status.EvaluationError)
	}

	newRules := review.Status.ResourceRules

	w.mu.Lock()
	oldRules := w.rules
	firstSync := !w.hasSynced
	rulesChanged := firstSync || !rulesEqual(oldRules, newRules)
	w.rules = copyRules(newRules)
	w.hasSynced = true
	handlers := slices.Clone(w.handlers)
	w.mu.Unlock()

	if rulesChanged && len(handlers) > 0 {
		for _, handler := range handlers {
			handler(oldRules, newRules)
		}
	}

	return nil
}

// Run starts the periodic rules sync until the context is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.Sync(ctx); err != nil {
		klog.FromContext(ctx).Error(err, "Failed initial rules sync")
	}

	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := w.Sync(ctx); err != nil {
			klog.FromContext(ctx).Error(err, "Failed to refresh rules")
		}
	}, w.resyncPeriod)

	return nil
}

func ruleAllows(rule authv1.ResourceRule, verb, apiGroup, resource, resourceName string) bool {
	if !matchWildcardOrString(rule.Verbs, verb) {
		return false
	}
	if !matchWildcardOrString(rule.APIGroups, apiGroup) {
		return false
	}
	if !matchWildcardOrString(rule.Resources, resource) {
		return false
	}
	if resourceName == "" {
		return len(rule.ResourceNames) == 0
	}
	if len(rule.ResourceNames) == 0 {
		return true
	}
	return slices.Contains(rule.ResourceNames, resourceName)
}

func matchWildcardOrString(slice []string, val string) bool {
	return slices.Contains(slice, "*") || slices.Contains(slice, val)
}

func copyRules(rules []authv1.ResourceRule) []authv1.ResourceRule {
	if rules == nil {
		return nil
	}
	result := make([]authv1.ResourceRule, len(rules))
	for i, r := range rules {
		result[i] = authv1.ResourceRule{
			Verbs:         slices.Clone(r.Verbs),
			APIGroups:     slices.Clone(r.APIGroups),
			Resources:     slices.Clone(r.Resources),
			ResourceNames: slices.Clone(r.ResourceNames),
		}
	}
	return result
}

func rulesEqual(r1, r2 []authv1.ResourceRule) bool {
	if len(r1) != len(r2) {
		return false
	}
	if len(r1) == 0 {
		return true
	}

	norm1 := normalizeRules(r1)
	norm2 := normalizeRules(r2)

	return slices.EqualFunc(norm1, norm2, singleRuleEqual)
}

func normalizeRules(rules []authv1.ResourceRule) []authv1.ResourceRule {
	normalized := make([]authv1.ResourceRule, len(rules))
	for i, r := range rules {
		rule := authv1.ResourceRule{
			Verbs:         slices.Clone(r.Verbs),
			APIGroups:     slices.Clone(r.APIGroups),
			Resources:     slices.Clone(r.Resources),
			ResourceNames: slices.Clone(r.ResourceNames),
		}
		slices.Sort(rule.Verbs)
		slices.Sort(rule.APIGroups)
		slices.Sort(rule.Resources)
		slices.Sort(rule.ResourceNames)
		normalized[i] = rule
	}
	slices.SortFunc(normalized, func(a, b authv1.ResourceRule) int {
		return strings.Compare(ruleKey(a), ruleKey(b))
	})
	return normalized
}

func ruleKey(r authv1.ResourceRule) string {
	return fmt.Sprintf("g:%s;r:%s;v:%s;n:%s",
		strings.Join(r.APIGroups, ","),
		strings.Join(r.Resources, ","),
		strings.Join(r.Verbs, ","),
		strings.Join(r.ResourceNames, ","),
	)
}

func singleRuleEqual(a, b authv1.ResourceRule) bool {
	return slices.Equal(a.Verbs, b.Verbs) &&
		slices.Equal(a.APIGroups, b.APIGroups) &&
		slices.Equal(a.Resources, b.Resources) &&
		slices.Equal(a.ResourceNames, b.ResourceNames)
}
