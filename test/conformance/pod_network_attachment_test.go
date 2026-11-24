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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
)

var _ = Describe("Pod Network Attachment", func() {
	It("should create a network from manifest, attach a pod to it via ResourceClaim, and verify status in ResourceClaim", func(ctx context.Context) {
		// Resolve the implementation resource type and its scope via API discovery
		groupResources, err := restmapper.GetAPIGroupResources(kubeClient.Discovery())
		Expect(err).NotTo(HaveOccurred(), "failed to get API group resources from discovery")

		mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: implementationGroup, Kind: implementationKind})
		Expect(err).NotTo(HaveOccurred(), "failed to resolve REST mapping for group %s and kind %s", implementationGroup, implementationKind)

		isNamespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace

		By(fmt.Sprintf("installing network instance from manifest %s", networkManifest))
		yamlBytes, readErr := os.ReadFile(networkManifest)
		Expect(readErr).NotTo(HaveOccurred(), "failed to read network manifest: %s", networkManifest)

		var networkName string
		var networkNamespace string

		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(yamlBytes), 4096)
		for {
			obj := &unstructured.Unstructured{}
			decodeErr := decoder.Decode(obj)
			if decodeErr == io.EOF {
				break
			}
			if decodeErr != nil || len(obj.Object) == 0 {
				continue
			}

			gvk := obj.GroupVersionKind()
			targetGVR := schema.GroupVersionResource{
				Group:    gvk.Group,
				Version:  gvk.Version,
				Resource: mapping.Resource.Resource,
			}

			ns := obj.GetNamespace()
			if isNamespaced && ns == "" {
				ns = testNamespace
				obj.SetNamespace(ns)
			}

			var created *unstructured.Unstructured
			if isNamespaced {
				created, err = dynamicClient.Resource(targetGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
			} else {
				created, err = dynamicClient.Resource(targetGVR).Create(ctx, obj, metav1.CreateOptions{})
			}
			Expect(err).NotTo(HaveOccurred(), "failed to create object %s/%s from manifest", obj.GetKind(), obj.GetName())

			createdName := created.GetName()
			createdNamespace := created.GetNamespace()
			DeferCleanup(func(ctx context.Context) {
				if isNamespaced {
					_ = dynamicClient.Resource(targetGVR).Namespace(createdNamespace).Delete(ctx, createdName, metav1.DeleteOptions{})
				} else {
					_ = dynamicClient.Resource(targetGVR).Delete(ctx, createdName, metav1.DeleteOptions{})
				}
			})

			if gvk.Group == implementationGroup && gvk.Kind == implementationKind {
				networkName = createdName
				networkNamespace = createdNamespace
			}
		}
		Expect(networkName).NotTo(BeEmpty(), "no network object matching group %s and kind %s found in manifest", implementationGroup, implementationKind)

		podNetworkKindName := podNetworkKind.Name
		deviceClassName := "conformance-" + podNetworkKindName

		By(fmt.Sprintf("ensuring DeviceClass %s exists", deviceClassName))
		_, err = kubeClient.ResourceV1().DeviceClasses().Create(ctx, &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: deviceClassName},
		}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred(), "failed to create DeviceClass")
		}
		DeferCleanup(func(ctx context.Context) {
			_ = kubeClient.ResourceV1().DeviceClasses().Delete(ctx, deviceClassName, metav1.DeleteOptions{})
		})

		domain := v1alpha1.StandardDeviceAttributePrefix
		celExpr := fmt.Sprintf(`device.attributes["%s"].podNetworkKind == "%s" && device.attributes["%s"].podNetwork == "%s"`,
			domain, podNetworkKindName, domain, networkName)
		if isNamespaced && networkNamespace != "" {
			celExpr += fmt.Sprintf(` && device.attributes["%s"].podNetworkNamespace == "%s"`, domain, networkNamespace)
		}

		By("creating a ResourceClaim selecting the pod network")
		claim := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "conformance-claim-",
				Namespace:    testNamespace,
			},
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{
						{
							Name: "network-device",
							Exactly: &resourcev1.ExactDeviceRequest{
								DeviceClassName: deviceClassName,
								Selectors: []resourcev1.DeviceSelector{
									{CEL: &resourcev1.CELDeviceSelector{Expression: celExpr}},
								},
							},
						},
					},
				},
			},
		}

		createdClaim, err := kubeClient.ResourceV1().ResourceClaims(testNamespace).Create(ctx, claim, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to create ResourceClaim")
		DeferCleanup(func(ctx context.Context) {
			_ = kubeClient.ResourceV1().ResourceClaims(testNamespace).Delete(ctx, createdClaim.Name, metav1.DeleteOptions{})
		})

		By("creating a Pod attached to the ResourceClaim")
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "conformance-pod-",
				Namespace:    testNamespace,
			},
			Spec: corev1.PodSpec{
				ResourceClaims: []corev1.PodResourceClaim{
					{
						Name:              "network",
						ResourceClaimName: &createdClaim.Name,
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "pause",
						Image: "registry.k8s.io/pause:3.10",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{
								{Name: "network"},
							},
						},
					},
				},
			},
		}

		createdPod, err := kubeClient.CoreV1().Pods(testNamespace).Create(ctx, pod, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to create Pod")
		DeferCleanup(func(ctx context.Context) {
			_ = kubeClient.CoreV1().Pods(testNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})
		})

		By("waiting for the pod to be running")
		Eventually(func(g Gomega) {
			p, err := kubeClient.CoreV1().Pods(testNamespace).Get(ctx, createdPod.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(p.Status.Phase).To(Equal(corev1.PodRunning), "pod phase is %s", p.Status.Phase)
		}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

		By("verifying the ResourceClaim device status reports network attachment details")
		Eventually(func(g Gomega) {
			c, err := kubeClient.ResourceV1().ResourceClaims(testNamespace).Get(ctx, createdClaim.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(c.Status.Devices).NotTo(BeEmpty(), "no devices allocated on ResourceClaim")

			for _, device := range c.Status.Devices {
				g.Expect(device.NetworkData).NotTo(BeNil(), "networkData should not be nil")
				g.Expect(device.NetworkData.InterfaceName).NotTo(BeEmpty(), "interface name must be reported")

				g.Expect(device.Data).NotTo(BeNil(), "device.data must not be nil")
				g.Expect(device.Data.Raw).NotTo(BeEmpty(), "device.data.raw must not be empty")

				var deviceStatus v1alpha1.DeviceStatus
				jsonErr := json.Unmarshal(device.Data.Raw, &deviceStatus)
				g.Expect(jsonErr).NotTo(HaveOccurred(), "failed to unmarshal device.data into DeviceStatus")
				g.Expect(deviceStatus.PodNetwork).NotTo(BeNil(), "data.podNetwork must be present in device status")

				podNetwork := deviceStatus.PodNetwork
				g.Expect(podNetwork.Name).To(Equal(networkName), "podNetwork.name mismatch")
				g.Expect(podNetwork.Kind).To(Equal(podNetworkKindName), "podNetwork.kind mismatch")
				if isNamespaced {
					g.Expect(podNetwork.Namespace).NotTo(BeNil(), "podNetwork.namespace must be set for namespaced network")
					g.Expect(*podNetwork.Namespace).To(Equal(networkNamespace), "podNetwork.namespace mismatch")
				} else {
					g.Expect(podNetwork.Namespace).To(BeNil(), "podNetwork.namespace must not be set for cluster-scoped network")
				}
			}
		}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())

		By("verifying advertised device attributes in ResourceSlice")
		Eventually(func(g Gomega) {
			slices, listErr := kubeClient.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred(), "failed to list ResourceSlices")

			var matchedDevice *resourcev1.Device
			for _, slice := range slices.Items {
				for i := range slice.Spec.Devices {
					dev := &slice.Spec.Devices[i]
					netAttr, hasNet := dev.Attributes[resourcev1.QualifiedName(v1alpha1.StandardDeviceAttributePodNetwork)]
					if !hasNet || netAttr.StringValue == nil || *netAttr.StringValue != networkName {
						continue
					}
					kindAttr, hasKind := dev.Attributes[resourcev1.QualifiedName(v1alpha1.StandardDeviceAttributePodNetworkKind)]
					if hasKind && kindAttr.StringValue != nil && *kindAttr.StringValue != podNetworkKindName {
						continue
					}
					matchedDevice = dev
					break
				}
				if matchedDevice != nil {
					break
				}
			}

			g.Expect(matchedDevice).NotTo(BeNil(), "advertised device for network %q not found in ResourceSlices", networkName)

			nsAttr, hasNs := matchedDevice.Attributes[resourcev1.QualifiedName(v1alpha1.StandardDeviceAttributePodNetworkNamespace)]
			if isNamespaced {
				g.Expect(hasNs).To(BeTrue(), "%s must be set for namespaced network", v1alpha1.StandardDeviceAttributePodNetworkNamespace)
				g.Expect(nsAttr.StringValue).NotTo(BeNil())
				g.Expect(*nsAttr.StringValue).To(Equal(networkNamespace))
			} else {
				g.Expect(hasNs).To(BeFalse(), "%s must not exist for cluster-scoped network", v1alpha1.StandardDeviceAttributePodNetworkNamespace)
			}
		}).WithContext(ctx).WithTimeout(Timeout).WithPolling(Polling).Should(Succeed())
	})
})
