/*
Copyright 2024 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package inventory

import (
	"context"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/multi-network/internal/podnet"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/time/rate"
	resourceapi "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const (
	networkKind = "network"
	rdmaKind    = "rdma"

	// database poll period
	minInterval = 5 * time.Second
	maxInterval = 1 * time.Minute
)

var (
	dns1123LabelNonValid = regexp.MustCompile("[^a-z0-9-]")
)

type DB struct {
	mu       sync.RWMutex
	podStore map[int]string // key: netnsid path value: Pod namespace/name

	rateLimiter   *rate.Limiter
	notifications chan []resourceapi.Device

	pnShare *podnet.PNShare
}

type Device struct {
	Kind string
	Name string
}

func New(pnShare *podnet.PNShare) *DB {
	return &DB{
		rateLimiter:   rate.NewLimiter(rate.Every(minInterval), 1),
		podStore:      map[int]string{},
		notifications: make(chan []resourceapi.Device),

		pnShare: pnShare,
	}
}

func (db *DB) AddPodNetns(pod string, netnsPath string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	ns, err := netns.GetFromPath(netnsPath)
	if err != nil {
		klog.Infof("fail to get pod %s network namespace %s handle: %v", pod, netnsPath, err)
		return
	}
	defer ns.Close()
	id, err := netlink.GetNetNsIdByFd(int(ns))
	if err != nil {
		klog.Infof("fail to get pod %s network namespace %s netnsid: %v", pod, netnsPath, err)
		return
	}
	db.podStore[id] = pod
}

func (db *DB) RemovePodNetns(pod string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for k, v := range db.podStore {
		if v == pod {
			delete(db.podStore, k)
			return
		}
	}
}

func (db *DB) GetPodName(netnsid int) string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.podStore[netnsid]
}

func (db *DB) Run(ctx context.Context) error {
	defer close(db.notifications)
	// Resources are published periodically or if there is a netlink notification
	// indicating a new interfaces was added or changed
	nlChannel := make(chan netlink.LinkUpdate)
	doneCh := make(chan struct{})
	defer close(doneCh)
	if err := netlink.LinkSubscribe(nlChannel, doneCh); err != nil {
		klog.Error(err, "error subscribing to netlink interfaces, only syncing periodically", "interval", maxInterval.String())
	}

	for {
		err := db.rateLimiter.Wait(ctx)
		if err != nil {
			klog.Error(err, "unexpected rate limited error trying to get system interfaces")
		}

		devices := []resourceapi.Device{}
		ifaces, err := net.Interfaces()
		if err != nil {
			klog.Error(err, "unexpected error trying to get system interfaces")
		}
		for _, iface := range ifaces {
			klog.V(7).InfoS("Checking network interface", "name", iface.Name)

			// skip loopback interfaces
			if iface.Flags&net.FlagLoopback != 0 {
				continue
			}

			// publish this network interface
			device, err := db.netdevToDRAdev(iface.Name)
			if err != nil {
				klog.V(2).Infof("could not obtain attributes for iface %s : %v", iface.Name, err)
				continue
			}

			devices = append(devices, *device)
			klog.V(4).Infof("Found following network interface %s", iface.Name)
		}

		klog.V(4).Infof("Found %d devices", len(devices))
		if len(devices) > 0 {
			db.notifications <- devices
		}
		select {
		// trigger a reconcile
		case <-nlChannel:
			// drain the channel so we only sync once
			for len(nlChannel) > 0 {
				<-nlChannel
			}
		case <-db.pnShare.PodNetworkTrigger:
			// drain the channel so we only sync once
			for len(db.pnShare.PodNetworkTrigger) > 0 {
				<-db.pnShare.PodNetworkTrigger
			}
		case <-time.After(maxInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (db *DB) GetResources(ctx context.Context) <-chan []resourceapi.Device {
	return db.notifications
}

func (db *DB) netdevToDRAdev(ifName string) (*resourceapi.Device, error) {
	device := resourceapi.Device{
		Name: ifName,
		Basic: &resourceapi.BasicDevice{
			Attributes: make(map[resourceapi.QualifiedName]resourceapi.DeviceAttribute),
			Capacity:   make(map[resourceapi.QualifiedName]resourceapi.DeviceCapacity),
		},
	}
	// normalize the name because interface names may contain invalid
	// characters as object names
	if len(validation.IsDNS1123Label(ifName)) > 0 {
		klog.V(2).Infof("normalizing iface %s name", ifName)
		device.Name = "normalized-" + dns1123LabelNonValid.ReplaceAllString(ifName, "-")
	}
	device.Basic.Attributes["dra.net/kind"] = resourceapi.DeviceAttribute{StringValue: ptr.To(networkKind)}
	device.Basic.Attributes["dra.net/name"] = resourceapi.DeviceAttribute{StringValue: &ifName}
	link, err := netlink.LinkByName(ifName)
	if err != nil {
		klog.Infof("Error getting link by name %v", err)
		return nil, err
	}
	linkType := link.Type()
	linkAttrs := link.Attrs()

	db.pnShare.Lock.Lock()
	defer db.pnShare.Lock.Unlock()
	for k, v := range db.pnShare.DranetData {
		if v.Name == ifName {
			device.Basic.Attributes["dra.net/podNetwork"] = resourceapi.DeviceAttribute{StringValue: &k}
			break
		}
	}

	// identify the namespace holding the link as the other end of a veth pair
	netnsid := link.Attrs().NetNsID
	if podName := db.GetPodName(netnsid); podName != "" {
		device.Basic.Attributes["dra.net/pod"] = resourceapi.DeviceAttribute{StringValue: &podName}
	}

	v4 := sets.Set[string]{}
	v6 := sets.Set[string]{}
	if ips, err := netlink.AddrList(link, netlink.FAMILY_ALL); err == nil && len(ips) > 0 {
		for _, address := range ips {
			if !address.IP.IsGlobalUnicast() {
				continue
			}

			if address.IP.To4() == nil && address.IP.To16() != nil {
				v6.Insert(address.IP.String())
			} else if address.IP.To4() != nil {
				v4.Insert(address.IP.String())
			}
		}
		if v4.Len() > 0 {
			device.Basic.Attributes["dra.net/ipv4"] = resourceapi.DeviceAttribute{StringValue: ptr.To(strings.Join(v4.UnsortedList(), ","))}
		}
		if v6.Len() > 0 {
			device.Basic.Attributes["dra.net/ipv6"] = resourceapi.DeviceAttribute{StringValue: ptr.To(strings.Join(v6.UnsortedList(), ","))}
		}
		mac := link.Attrs().HardwareAddr.String()
		device.Basic.Attributes["dra.net/mac"] = resourceapi.DeviceAttribute{StringValue: &mac}
		mtu := int64(link.Attrs().MTU)
		device.Basic.Attributes["dra.net/mtu"] = resourceapi.DeviceAttribute{IntValue: &mtu}
	}

	device.Basic.Attributes["dra.net/encapsulation"] = resourceapi.DeviceAttribute{StringValue: &linkAttrs.EncapType}
	operState := linkAttrs.OperState.String()
	device.Basic.Attributes["dra.net/state"] = resourceapi.DeviceAttribute{StringValue: &operState}
	device.Basic.Attributes["dra.net/alias"] = resourceapi.DeviceAttribute{StringValue: &linkAttrs.Alias}
	device.Basic.Attributes["dra.net/type"] = resourceapi.DeviceAttribute{StringValue: &linkType}

	return &device, nil
}
