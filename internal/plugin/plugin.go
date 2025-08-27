package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"sigs.k8s.io/multi-network/internal/inventory"
	"sigs.k8s.io/multi-network/internal/podnet"
	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	// "k8s.io/client-go/tools/cache"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
)

const (
	kubeletPluginRegistryPath = "/var/lib/kubelet/plugins_registry"
	kubeletPluginPath         = "/var/lib/kubelet/plugins"
	podUIDIndex               = "podUID"
)

func podUIDIndexFunc(obj interface{}) ([]string, error) {
	claim, ok := obj.(*resourcev1beta1.ResourceClaim)
	if !ok {
		return []string{}, nil
	}

	result := []string{}
	for _, reserved := range claim.Status.ReservedFor {
		if reserved.Resource != "pods" || reserved.APIGroup != "" {
			continue
		}
		result = append(result, string(reserved.UID))
	}
	return result, nil
}

type podStatus struct {
	PodNetwork string `json:"podNetwork"`
}

// NetworkDriver implements both the DRA and NRI gRPC server interfaces.
type NetworkDriver struct {
	driverName       string
	kubeClient       kubernetes.Interface
	nodeName         string
	draHelper        *kubeletplugin.Helper
	nriPlugin        stub.Stub
	claimAllocations cache.Indexer // claims indexed by Claim UID to run on the Kubelet/DRA hooks
	// contains the host interfaces
	netdb   *inventory.DB
	localdb map[string]resourcev1beta1.Device
}

func Start(ctx context.Context, driverName string, nodeName string, pnShare *podnet.PNShare, kubeClient kubernetes.Interface) (*NetworkDriver, error) {
	store := cache.NewIndexer(cache.MetaNamespaceKeyFunc,
		cache.Indexers{
			cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
			podUIDIndex:          podUIDIndexFunc,
		})

	driverPluginPath := filepath.Join(kubeletPluginPath, driverName)
	err := os.MkdirAll(driverPluginPath, 0750)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %v", driverPluginPath, err)
	}
	d := &NetworkDriver{
		driverName:       driverName,
		kubeClient:       kubeClient,
		nodeName:         nodeName,
		claimAllocations: store,
		localdb:          make(map[string]resourcev1beta1.Device),
	}
	helper, err := kubeletplugin.Start(
		ctx,
		d,
		kubeletplugin.KubeClient(d.kubeClient),
		kubeletplugin.NodeName(d.nodeName),
		kubeletplugin.DriverName(driverName),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start DRA plugin: %w", err)
	}
	d.draHelper = helper
	klog.Info("DRA plugin started successfully")
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		status := helper.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		return status.PluginRegistered, nil
	})
	if err != nil {
		return nil, err
	}
	// Start NRI plugin
	nriOpts := []stub.Option{
		stub.WithPluginName(driverName),
		stub.WithPluginIdx("00"),
		// stub.WithSocketPath(filepath.Join(driverPluginPath, "nri.sock")),
	}
	nriPlugin, err := stub.New(d, nriOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create NRI plugin stub: %w", err)
	}
	d.nriPlugin = nriPlugin
	go func() {
		if err := d.nriPlugin.Run(ctx); err != nil {
			klog.Errorf("NRI plugin failed: %v", err)
		}
	}()
	klog.Info("NRI plugin started successfully")

	d.netdb = inventory.New(pnShare)
	go func() {
		err = d.netdb.Run(ctx)
		if err != nil {
			klog.Infof("Network Device DB failed with error %v", err)
		}
	}()
	go d.PublishResources(ctx)
	return d, nil
}

func (d *NetworkDriver) Stop() {
	d.nriPlugin.Stop()
	d.draHelper.Stop()
}

// --- DRA Methods ---

func (d *NetworkDriver) PrepareResourceClaims(ctx context.Context, claims []*resourcev1beta1.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	klog.Infof("PrepareResourceClaims is called: number of claims: %d", len(claims))
	result := make(map[types.UID]kubeletplugin.PrepareResult)

	for _, claim := range claims {
		result[claim.UID] = d.prepareResourceClaim(ctx, claim)
		err := d.claimAllocations.Add(claim)
		if err != nil {
			return nil, fmt.Errorf("failed to add claim %s/%s to local cache: %w", claim.Namespace, claim.Name, err)
		}
	}

	return result, nil
}

func (d *NetworkDriver) prepareResourceClaim(_ context.Context, claim *resourcev1beta1.ResourceClaim) kubeletplugin.PrepareResult {
	var devices []kubeletplugin.Device
	for _, result := range claim.Status.Allocation.Devices.Results {
		requestName := result.Request
		for _, config := range claim.Status.Allocation.Devices.Config {
			if config.Opaque == nil ||
				config.Opaque.Driver != d.driverName ||
				len(config.Requests) > 0 && !slices.Contains(config.Requests, requestName) {
				continue
			}
		}
		device := kubeletplugin.Device{
			PoolName:   result.Pool,
			DeviceName: result.Device,
		}
		devices = append(devices, device)
	}

	klog.Infof("Returning newly prepared devices for claim '%v': %v", claim.UID, devices)
	return kubeletplugin.PrepareResult{Devices: devices}
}

func (d *NetworkDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	klog.Infof("UnprepareResourceClaims is called: number of claims: %d", len(claims))
	result := make(map[types.UID]error)

	for _, claim := range claims {
		result[claim.UID] = d.unprepareResourceClaim(ctx, claim)
	}

	return result, nil
}

func (d *NetworkDriver) unprepareResourceClaim(_ context.Context, claim kubeletplugin.NamespacedObject) error {
	objs, err := d.claimAllocations.ByIndex(cache.NamespaceIndex, fmt.Sprintf("%s/%s", claim.Namespace, claim.Name))
	if err != nil || len(objs) == 0 {
		klog.Infof("Claim %s/%s does not have an associated cached ResourceClaim: %v", claim.Namespace, claim.Name, err)
		return nil
	}
	for _, obj := range objs {
		claim, ok := obj.(*resourcev1beta1.ResourceClaim)
		if !ok {
			continue
		}
		defer func() {
			err := d.claimAllocations.Delete(obj)
			if err != nil {
				klog.Infof("Claim %s/%s can not be deleted from cache: %v", claim.Namespace, claim.Name, err)
			}
		}()

		if claim.Status.Allocation == nil {
			continue
		}

		for _, result := range claim.Status.Allocation.Devices.Results {
			if result.Driver != d.driverName {
				continue
			}

			for _, config := range claim.Status.Allocation.Devices.Config {
				if config.Opaque == nil {
					continue
				}
				klog.V(4).Infof("nodeUnprepareResource Configuration %s", string(config.Opaque.Parameters.String()))
				// TODO get config options here, it can add ips or commands
				// to add routes, run dhcp, rename the interface ... whatever
			}
			klog.Infof("nodeUnprepareResource claim %s/%s with allocation result %#v", claim.Namespace, claim.Name, result)

		}
	}
	return nil
}

// --- NRI Methods ---

func (d *NetworkDriver) Synchronize(_ context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	klog.Infof("Synchronized state with the runtime (%d pods, %d containers)...",
		len(pods), len(containers))

	for _, pod := range pods {
		klog.Infof("pod %s/%s: namespace=%s ips=%v", pod.GetNamespace(), pod.GetName(), getNetworkNamespace(pod), pod.GetIps())
		// get the pod network namespace
		ns := getNetworkNamespace(pod)
		// host network pods are skipped
		if ns != "" {
			// store the Pod metadata in the db
			d.netdb.AddPodNetns(podKey(pod), ns)
		}
	}

	return nil, nil
}

func (d *NetworkDriver) Shutdown(_ context.Context) {
	klog.Info("Runtime shutting down...")
}

func (d *NetworkDriver) RunPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("RunPodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
	objs, err := d.claimAllocations.ByIndex(podUIDIndex, pod.Uid)
	if err != nil || len(objs) == 0 {
		klog.V(4).Infof("RunPodSandbox Pod %s/%s does not have an associated ResourceClaim", pod.Namespace, pod.Name)
		return nil
	}

	// get the pod network namespace
	ns := getNetworkNamespace(pod)
	// host network pods are skipped
	if ns == "" {
		klog.V(2).Infof("RunPodSandbox pod %s/%s using host network, skipping", pod.Namespace, pod.Name)
		return nil
	}
	// store the Pod metadata in the db
	d.netdb.AddPodNetns(podKey(pod), ns)

	// Process the configurations of the ResourceClaim
	for _, obj := range objs {
		claim, ok := obj.(*resourcev1beta1.ResourceClaim)
		if !ok {
			continue
		}

		if claim.Status.Allocation == nil {
			continue
		}
		var devivcesStatus []resourcev1beta1.AllocatedDeviceStatus
		for _, result := range claim.Status.Allocation.Devices.Results {
			if result.Driver != d.driverName {
				continue
			}
			devState := resourcev1beta1.AllocatedDeviceStatus{
				Driver: result.Driver,
				Pool:   result.Pool,
				Device: result.Device,
			}

			// Process the configurations of the ResourceClaim
			for _, config := range claim.Status.Allocation.Devices.Config {
				if config.Opaque == nil {
					continue
				}
				if len(config.Requests) > 0 && !slices.Contains(config.Requests, result.Request) {
					continue
				}
				klog.V(4).Infof("podStartHook Configuration %s", string(config.Opaque.Parameters.String()))
				// TODO get config options here, it can add ips or commands
				// to add routes, run dhcp, rename the interface ... whatever
			}

			klog.Infof("RunPodSandbox allocation.Devices.Result: %#v", result)

			// TODO config options to rename the device and pass parameters
			// use https://github.com/opencontainers/runtime-spec/pull/1271
			ifcData, err := nsAttachNetdev(result.Device, ns, result.Device)
			if err != nil {
				klog.Infof("RunPodSandbox error moving device %s to namespace %s: %v", result.Device, ns, err)
				return err
			}

			if device, ok := d.localdb[result.Device]; ok {
				if podNet, ok := device.Basic.Attributes["dra.net/podNetwork"]; ok {
					data, err := json.Marshal(&podStatus{
						PodNetwork: *podNet.StringValue,
					})
					if err != nil {
						klog.Infof("RunPodSandbox error marshaling device %s data status: %v", result.Device, err)
						return err
					}
					devState.Data = &runtime.RawExtension{Raw: data}
				}
			} else {
				klog.Warningf("Device %s not found in local DB", result.Device)
			}

			devState.NetworkData = ifcData
			devivcesStatus = append(devivcesStatus, devState)
		}
		claim.Status.Devices = devivcesStatus
		_, err := d.kubeClient.ResourceV1beta1().ResourceClaims(claim.Namespace).UpdateStatus(ctx, claim, metav1.UpdateOptions{})
		if err != nil {
			klog.Infof("RunPodSandbox error updating claim %s/%s : %v", claim.Namespace, claim.Name, err)
			return err
		}
	}
	return nil
}

func (d *NetworkDriver) StopPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("StopPodSandbox pod %s/%s", pod.Namespace, pod.Name)
	defer d.netdb.RemovePodNetns(podKey(pod))

	objs, err := d.claimAllocations.ByIndex(podUIDIndex, pod.Uid)
	if err != nil || len(objs) == 0 {
		klog.V(2).Infof("StopPodSandbox pod %s/%s does not have allocations", pod.Namespace, pod.Name)
		return nil
	}

	// get the pod network namespace
	ns := getNetworkNamespace(pod)
	if ns == "" {
		klog.V(2).Infof("StopPodSandbox pod %s/%s using host network, skipping", pod.Namespace, pod.Name)
		return nil
	}
	// Process the configurations of the ResourceClaim
	for _, obj := range objs {
		claim, ok := obj.(*resourcev1beta1.ResourceClaim)
		if !ok {
			continue
		}

		if claim.Status.Allocation == nil {
			continue
		}

		for _, result := range claim.Status.Allocation.Devices.Results {
			if result.Driver != d.driverName {
				continue
			}

			for _, config := range claim.Status.Allocation.Devices.Config {
				if config.Opaque == nil {
					continue
				}
				klog.V(4).Infof("podStopHook Configuration %s", string(config.Opaque.Parameters.String()))
				// TODO get config options here, it can add ips or commands
				// to add routes, run dhcp, rename the interface ... whatever
			}

			klog.V(4).Infof("podStopHook Device %s", result.Device)
			// TODO config options to rename the device and pass parameters
			// use https://github.com/opencontainers/runtime-spec/pull/1271
			err := nsDetachNetdev(ns, result.Device)
			if err != nil {
				klog.Infof("StopPodSandbox error moving device %s to namespace %s: %v", result.Device, ns, err)
				continue
			}
		}
	}
	return nil
}

func (np *NetworkDriver) RemovePodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("RemovePodSandbox pod %s/%s: ips=%v", pod.GetNamespace(), pod.GetName(), pod.GetIps())
	// get the pod network namespace
	ns := getNetworkNamespace(pod)
	if ns == "" {
		klog.V(2).Infof("RemovePodSandbox pod %s/%s using host network, skipping", pod.Namespace, pod.Name)
		return nil
	}
	return nil
}

func getNetworkNamespace(pod *api.PodSandbox) string {
	// get the pod network namespace
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			return namespace.Path
		}
	}
	return ""
}

func (d *NetworkDriver) updateLocalDB(devices []resourcev1beta1.Device) {
	d.localdb = map[string]resourcev1beta1.Device{}
	for _, device := range devices {
		d.localdb[device.Name] = device
	}
}

func (d *NetworkDriver) PublishResources(ctx context.Context) {
	klog.V(2).Infof("Publishing resources")
	for {
		select {
		case devices := <-d.netdb.GetResources(ctx):
			klog.V(4).Infof("Received %d devices", len(devices))
			d.updateLocalDB(devices)
			resources := resourceslice.DriverResources{
				Pools: map[string]resourceslice.Pool{
					d.nodeName: {
						Slices: []resourceslice.Slice{
							{
								Devices: devices,
							},
						},
					},
				},
			}
			err := d.draHelper.PublishResources(ctx, resources)
			if err != nil {
				klog.Error(err, "unexpected error trying to publish resources")
			}
		case <-ctx.Done():
			klog.Error(ctx.Err(), "context canceled")
			return
		}
		// poor man rate limit
		time.Sleep(3 * time.Second)
	}
}

func podKey(pod *api.PodSandbox) string {
	return fmt.Sprintf("%s/%s", pod.GetNamespace(), pod.GetName())
}
