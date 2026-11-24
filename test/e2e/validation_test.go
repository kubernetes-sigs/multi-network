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

package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
)

var _ = Describe("PodNetworkKind Validation", func() {
	Context("immutability", func() {
		It("should not allow modifying spec.implementationType", func(ctx context.Context) {
			pnkName := "e2e-val-immutable-impl-type"
			pnk := &v1alpha1.PodNetworkKind{
				ObjectMeta: metav1.ObjectMeta{
					Name: pnkName,
				},
				Spec: v1alpha1.PodNetworkKindSpec{
					ImplementationType: metav1.GroupKind{
						Group: "example.com",
						Kind:  "InitialNetwork",
					},
				},
			}

			By("creating the PodNetworkKind")
			_, err := multinetworkClient.PodNetworkKinds().Create(ctx, pnk, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to create PodNetworkKind")

			DeferCleanup(func(ctx context.Context) {
				_ = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkName, metav1.DeleteOptions{})
			})

			By("attempting to update spec.implementationType.group")
			Eventually(func(g Gomega) {
				current, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				toUpdateGroup := current.DeepCopy()
				toUpdateGroup.Spec.ImplementationType.Group = "updated.example.com"
				_, err = multinetworkClient.PodNetworkKinds().Update(ctx, toUpdateGroup, metav1.UpdateOptions{})
				g.Expect(err).To(HaveOccurred(), "expected error when updating implementationType.group")
				if apierrors.IsConflict(err) {
					return
				}
				g.Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid error, got: %v", err)
				g.Expect(err.Error()).To(ContainSubstring("spec.implementationType is immutable"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("attempting to update spec.implementationType.kind")
			Eventually(func(g Gomega) {
				current, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				toUpdateKind := current.DeepCopy()
				toUpdateKind.Spec.ImplementationType.Kind = "UpdatedNetwork"
				_, err = multinetworkClient.PodNetworkKinds().Update(ctx, toUpdateKind, metav1.UpdateOptions{})
				g.Expect(err).To(HaveOccurred(), "expected error when updating implementationType.kind")
				if apierrors.IsConflict(err) {
					return
				}
				g.Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid error, got: %v", err)
				g.Expect(err.Error()).To(ContainSubstring("spec.implementationType is immutable"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
		})

		It("should allow updating metadata (labels, annotations)", func(ctx context.Context) {
			pnkName := "e2e-val-mutable-metadata"
			pnk := &v1alpha1.PodNetworkKind{
				ObjectMeta: metav1.ObjectMeta{
					Name: pnkName,
				},
				Spec: v1alpha1.PodNetworkKindSpec{
					ImplementationType: metav1.GroupKind{
						Group: "example.com",
						Kind:  "InitialNetwork",
					},
				},
			}

			By("creating the PodNetworkKind")
			_, err := multinetworkClient.PodNetworkKinds().Create(ctx, pnk, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to create PodNetworkKind")

			DeferCleanup(func(ctx context.Context) {
				_ = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkName, metav1.DeleteOptions{})
			})

			By("updating labels and annotations")
			Eventually(func(g Gomega) {
				current, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())

				toUpdate := current.DeepCopy()
				if toUpdate.Labels == nil {
					toUpdate.Labels = make(map[string]string)
				}
				toUpdate.Labels["test-label"] = "updated-value"

				if toUpdate.Annotations == nil {
					toUpdate.Annotations = make(map[string]string)
				}
				toUpdate.Annotations["test-annotation"] = "updated-value"

				updated, err := multinetworkClient.PodNetworkKinds().Update(ctx, toUpdate, metav1.UpdateOptions{})
				if apierrors.IsConflict(err) {
					return
				}
				g.Expect(err).NotTo(HaveOccurred(), "failed to update metadata")
				g.Expect(updated.Labels).To(HaveKeyWithValue("test-label", "updated-value"))
				g.Expect(updated.Annotations).To(HaveKeyWithValue("test-annotation", "updated-value"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
		})
	})
})
