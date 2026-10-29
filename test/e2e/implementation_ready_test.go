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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
)

var _ = Describe("PodNetworkKind ImplementationTypeReady Condition", func() {
	Context("Resource Type presence (ResourceTypeNotFound)", func() {
		It("should report False when Resource Type is missing, turn True when created, and turn back False when deleted", func(ctx context.Context) {
			group := "resourcetype.presence.e2e.example.com"
			kind := "PresenceNet"
			plural := "presencenets"
			resourceTypeName := plural + "." + group
			pnkName := "e2e-resource-type-presence"

			By("granting RBAC permissions to the controller in advance")
			grantRBAC(ctx, "e2e-presence-reader", group, plural)
			DeferCleanup(func(ctx context.Context) {
				deleteRBAC(ctx, "e2e-presence-reader")
			})

			pnk := &v1alpha1.PodNetworkKind{
				ObjectMeta: metav1.ObjectMeta{Name: pnkName},
				Spec: v1alpha1.PodNetworkKindSpec{
					ImplementationType: metav1.GroupKind{Group: group, Kind: kind},
				},
			}

			By("creating the PodNetworkKind before the Resource Type exists")
			_, err := multinetworkClient.PodNetworkKinds().Create(ctx, pnk, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to create PodNetworkKind")

			DeferCleanup(func(ctx context.Context) {
				_ = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkName, metav1.DeleteOptions{})
			})

			By("waiting for ImplementationTypeReady=False with reason ResourceTypeNotFound")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found")
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonResourceTypeNotFound))
				g.Expect(cond.Message).To(Equal("Resource Type not found"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("creating the compliant Resource Type")
			createCRD(ctx, resourceTypeName, group, kind, plural)

			By("waiting for ImplementationTypeReady to turn True with reason Compliant")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found")
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonCompliant))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("deleting the Resource Type to make the condition unmet again")
			err = apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, resourceTypeName, metav1.DeleteOptions{})
			Expect(err).NotTo(HaveOccurred())

			By("waiting for ImplementationTypeReady to turn back False with reason ResourceTypeNotFound")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found")
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonResourceTypeNotFound))
				g.Expect(cond.Message).To(Equal("Resource Type not found"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
		})
	})

	Context("RBAC permissions (MissingRBAC)", func() {
		It("should report False when RBAC is missing, turn True when granted, and turn back False when revoked", func(ctx context.Context) {
			group := "resourcetype.rbac.e2e.example.com"
			kind := "RbacNet"
			plural := "rbacnets"
			resourceTypeName := plural + "." + group
			pnkName := "e2e-resource-type-rbac"
			rbacName := "e2e-rbac-test-reader"

			By("creating a compliant Resource Type without granting RBAC")
			createCRD(ctx, resourceTypeName, group, kind, plural)
			DeferCleanup(func(ctx context.Context) {
				_ = apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, resourceTypeName, metav1.DeleteOptions{})
			})

			pnk := &v1alpha1.PodNetworkKind{
				ObjectMeta: metav1.ObjectMeta{Name: pnkName},
				Spec: v1alpha1.PodNetworkKindSpec{
					ImplementationType: metav1.GroupKind{Group: group, Kind: kind},
				},
			}

			By("creating the PodNetworkKind referencing the Resource Type without RBAC")
			_, err := multinetworkClient.PodNetworkKinds().Create(ctx, pnk, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to create PodNetworkKind")

			DeferCleanup(func(ctx context.Context) {
				_ = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkName, metav1.DeleteOptions{})
			})

			By("waiting for ImplementationTypeReady=False with reason MissingRBAC")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updated.Status.Conditions).NotTo(BeEmpty())

				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found")
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonMissingRBAC))
				g.Expect(cond.Message).To(Equal("Insufficient permissions to access the Resources"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("granting RBAC permissions to the controller")
			grantRBAC(ctx, rbacName, group, plural)

			By("waiting for ImplementationTypeReady to turn True with reason Compliant")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found")
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonCompliant))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("revoking RBAC permissions to make the condition unmet again")
			deleteRBAC(ctx, rbacName)

			By("waiting for ImplementationTypeReady to turn back False with reason MissingRBAC")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil(), "ImplementationTypeReady condition not found")
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonMissingRBAC))
				g.Expect(cond.Message).To(Equal("Insufficient permissions to access the Resources"))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
		})
	})

	Context("Type Conflict (TypeConflict)", func() {
		It("should report TypeConflict on newer PodNetworkKind and Compliant on older PodNetworkKind, and turn True when conflict is deleted", func(ctx context.Context) {
			group := "resourcetype.conflict.e2e.example.com"
			kind := "ConflictNet"
			plural := "conflictnets"
			resourceTypeName := plural + "." + group
			pnkOlderName := "e2e-type-conflict-older"
			pnkNewerName := "e2e-type-conflict-newer"
			rbacName := "e2e-conflict-reader"

			By("granting RBAC permissions to the controller in advance")
			grantRBAC(ctx, rbacName, group, plural)
			DeferCleanup(func(ctx context.Context) {
				deleteRBAC(ctx, rbacName)
			})

			By("creating the compliant Resource Type")
			createCRD(ctx, resourceTypeName, group, kind, plural)
			DeferCleanup(func(ctx context.Context) {
				_ = apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, resourceTypeName, metav1.DeleteOptions{})
			})

			By("creating the first (older) PodNetworkKind")
			pnkOlder := &v1alpha1.PodNetworkKind{
				ObjectMeta: metav1.ObjectMeta{Name: pnkOlderName},
				Spec: v1alpha1.PodNetworkKindSpec{
					ImplementationType: metav1.GroupKind{Group: group, Kind: kind},
				},
			}
			_, err := multinetworkClient.PodNetworkKinds().Create(ctx, pnkOlder, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to create older PodNetworkKind")
			DeferCleanup(func(ctx context.Context) {
				_ = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkOlderName, metav1.DeleteOptions{})
			})

			By("waiting for the older PodNetworkKind to become Compliant")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkOlderName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonCompliant))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("creating the second (newer) PodNetworkKind referencing the same GroupKind")
			pnkNewer := &v1alpha1.PodNetworkKind{
				ObjectMeta: metav1.ObjectMeta{Name: pnkNewerName},
				Spec: v1alpha1.PodNetworkKindSpec{
					ImplementationType: metav1.GroupKind{Group: group, Kind: kind},
				},
			}
			_, err = multinetworkClient.PodNetworkKinds().Create(ctx, pnkNewer, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to create newer PodNetworkKind")
			DeferCleanup(func(ctx context.Context) {
				_ = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkNewerName, metav1.DeleteOptions{})
			})

			By("waiting for the newer PodNetworkKind to report TypeConflict")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkNewerName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonTypeConflict))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

			By("verifying the older PodNetworkKind remains Compliant")
			older, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkOlderName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			olderCond := meta.FindStatusCondition(older.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
			Expect(olderCond).NotTo(BeNil())
			Expect(olderCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(olderCond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonCompliant))

			By("deleting the older PodNetworkKind so the newer is promoted")
			err = multinetworkClient.PodNetworkKinds().Delete(ctx, pnkOlderName, metav1.DeleteOptions{})
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the remaining PodNetworkKind to become Compliant")
			Eventually(func(g Gomega) {
				updated, err := multinetworkClient.PodNetworkKinds().Get(ctx, pnkNewerName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				cond := meta.FindStatusCondition(updated.Status.Conditions, v1alpha1.PodNetworkKindConditionImplementationTypeReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal(v1alpha1.PodNetworkKindReasonCompliant))
			}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
		})
	})
})

func grantRBAC(ctx context.Context, name, group, resource string) {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{group},
				Resources: []string{resource},
				Verbs:     []string{"list"},
			},
		},
	}
	_, err := kubeClient.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-binding"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "multi-network-controller-service-account",
				Namespace: "default",
			},
		},
	}
	_, err = kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())
}

func deleteRBAC(ctx context.Context, name string) {
	_ = kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, name+"-binding", metav1.DeleteOptions{})
	_ = kubeClient.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
}

func buildCRD(name, group, kind, plural string) *apiextensionsv1.CustomResourceDefinition {
	names := apiextensionsv1.CustomResourceDefinitionNames{
		Plural:     plural,
		Singular:   strings.ToLower(kind),
		Kind:       kind,
		Categories: v1alpha1.Categories,
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: names,
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}
}

func createCRD(ctx context.Context, name, group, kind, plural string) *apiextensionsv1.CustomResourceDefinition {
	crd := buildCRD(name, group, kind, plural)
	created, err := apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		c, err := apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		var established bool
		for _, cond := range c.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				established = true
				break
			}
		}
		g.Expect(established).To(BeTrue(), "CRD is not established")
	}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

	return created
}
