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
	"flag"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/multi-network-api/apis/v1alpha1"
	multinetworkclient "sigs.k8s.io/multi-network-api/pkg/client/clientset/versioned/typed/apis/v1alpha1"
)

const (
	Timeout = 60 * time.Second
	Polling = 1 * time.Second
)

var (
	implementationGroup string
	implementationKind  string
	networkManifest     string
	testNamespace       string

	podNetworkKind *v1alpha1.PodNetworkKind

	kubeClient         kubernetes.Interface
	multinetworkClient *multinetworkclient.MultinetworkV1alpha1Client
	dynamicClient      dynamic.Interface
)

func init() {
	flag.StringVar(&implementationGroup, "implementation-group", "", "API group of the implementation CRD (e.g. devicenetwork.io)")
	flag.StringVar(&implementationKind, "implementation-kind", "", "Kind of the implementation CRD (e.g. DeviceNetwork)")
	flag.StringVar(&networkManifest, "network-manifest", "", "Path to YAML file defining the network instance to install")
	flag.StringVar(&testNamespace, "namespace", "default", "Namespace for test resources")
}

func TestConformance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multi-Network API Conformance Suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	Expect(implementationGroup).NotTo(BeEmpty(), "-implementation-group flag is required")
	Expect(implementationKind).NotTo(BeEmpty(), "-implementation-kind flag is required")
	Expect(networkManifest).NotTo(BeEmpty(), "-network-manifest flag is required")

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	Expect(err).NotTo(HaveOccurred(), "failed to load kubeconfig")

	kubeClient, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred(), "failed to create kube client")

	multinetworkClient, err = multinetworkclient.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred(), "failed to create multinetwork client")

	dynamicClient, err = dynamic.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred(), "failed to create dynamic client")

	list, err := multinetworkClient.PodNetworkKinds().List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to list PodNetworkKinds")

	var matching []v1alpha1.PodNetworkKind
	for _, item := range list.Items {
		if item.Spec.ImplementationType.Group == implementationGroup && item.Spec.ImplementationType.Kind == implementationKind {
			matching = append(matching, item)
		}
	}
	Expect(matching).NotTo(BeEmpty(), "no PodNetworkKind found matching group %q and kind %q", implementationGroup, implementationKind)
	Expect(matching).To(HaveLen(1), "expected exactly 1 PodNetworkKind matching group %q and kind %q, but found %d", implementationGroup, implementationKind, len(matching))

	podNetworkKind = &matching[0]
})
