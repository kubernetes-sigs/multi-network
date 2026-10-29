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

package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	podNetworkKindClientset "sigs.k8s.io/multi-network-api/pkg/client/clientset/versioned"
	podNetworkKindInformers "sigs.k8s.io/multi-network-api/pkg/client/informers/externalversions"
	"sigs.k8s.io/multi-network/pkg/controllers/podnetworkkind"
	"sigs.k8s.io/multi-network/pkg/ruleswatcher"
)

const (
	defaultInformerResyncPeriod = 30 * time.Second
)

type runOptions struct {
	verbosity int
}

func newCmdRun() *cobra.Command {
	runOpts := &runOptions{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the multi-network controller",
		Long:  `Run the multi-network controller`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runOpts.run(cmd.Context())
		},
	}

	cmd.Flags().IntVar(
		&runOpts.verbosity,
		"verbosity",
		0,
		"Log Level.",
	)

	return cmd
}

func (ro *runOptions) run(ctx context.Context) error {
	klog.InitFlags(nil)
	_ = flag.Set("v", fmt.Sprintf("%d", ro.verbosity))
	flag.Parse()

	clientCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to InClusterConfig: %v", err)
	}

	podNetworkKindClient, err := podNetworkKindClientset.NewForConfig(clientCfg)
	if err != nil {
		return fmt.Errorf("failed to NewForConfig: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(clientCfg)
	if err != nil {
		return fmt.Errorf("failed to NewForConfig: %v", err)
	}

	apiextensionsClient, err := apiextensionsclientset.NewForConfig(clientCfg)
	if err != nil {
		return fmt.Errorf("failed to NewForConfig: %v", err)
	}

	networkKindInformerFactory := podNetworkKindInformers.NewSharedInformerFactory(podNetworkKindClient, defaultInformerResyncPeriod)
	apiextensionsInformerFactory := apiextensionsinformers.NewSharedInformerFactory(apiextensionsClient, defaultInformerResyncPeriod)

	ruleWatcher := ruleswatcher.New(kubeClient)

	podNetworkKindController, err := podnetworkkind.NewPodNetworkKindController(
		networkKindInformerFactory.Multinetwork().V1alpha1().PodNetworkKinds(),
		apiextensionsInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		podNetworkKindClient.MultinetworkV1alpha1().PodNetworkKinds(),
		ruleWatcher,
		kubeClient,
	)
	if err != nil {
		return fmt.Errorf("failed to create network kind controller: %v", err)
	}

	networkKindInformerFactory.Start(ctx.Done())
	apiextensionsInformerFactory.Start(ctx.Done())

	go func() {
		err = ruleWatcher.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			klog.FromContext(ctx).Error(err, "failed to run rule watcher")
		}
	}()

	go func() {
		err = podNetworkKindController.Run(ctx, 1)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			klog.FromContext(ctx).Error(err, "failed to run network kind controller")
		}
	}()

	<-ctx.Done()

	return nil
}
