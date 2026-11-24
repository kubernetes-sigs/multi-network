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

package conformance

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
)

var _ = Describe("PodNetworkKind Conformance", func() {
	It("should be installed with the implementation and have ImplementationTypeReady condition set to True with reason Compliant", func(ctx context.Context) {
		Eventually(func(g Gomega) {
			pnk, err := multinetworkClient.PodNetworkKinds().Get(ctx, podNetworkKind.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred(), "PodNetworkKind %q should be installed", podNetworkKind.Name)
			g.Expect(pnk.Status.Conditions).NotTo(BeEmpty(), "no conditions set on PodNetworkKind %q", pnk.Name)

			cond := meta.FindStatusCondition(pnk.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
			g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found on PodNetworkKind %q", pnk.Name)
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue),
				"expected ImplementationTypeReady=True, got reason=%s message=%q", cond.Reason, cond.Message)
			g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonCompliant))
		}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
	})

	It("should reference a resource type that includes the required categories", func(ctx context.Context) {
		group := podNetworkKind.Spec.ImplementationType.Group
		kind := podNetworkKind.Spec.ImplementationType.Kind

		// Check discovery to verify that the resource type registers the required categories
		_, resourceLists, err := kubeClient.Discovery().ServerGroupsAndResources()
		if err != nil && len(resourceLists) == 0 {
			Expect(err).NotTo(HaveOccurred(), "failed to discover server resources")
		}

		var matchedResource *metav1.APIResource
		for _, rl := range resourceLists {
			gv, parseErr := schema.ParseGroupVersion(rl.GroupVersion)
			if parseErr != nil || gv.Group != group {
				continue
			}
			for i := range rl.APIResources {
				r := &rl.APIResources[i]
				if r.Kind == kind {
					matchedResource = r
					break
				}
			}
			if matchedResource != nil {
				break
			}
		}

		Expect(matchedResource).NotTo(BeNil(), "resource with group %q and kind %q not found in API discovery", group, kind)
		for _, category := range v1alpha1.Categories {
			Expect(matchedResource.Categories).To(ContainElement(category),
				"resource %s/%s must include the %q category", group, kind, category)
		}
	})
})
