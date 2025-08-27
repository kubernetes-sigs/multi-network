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

package plugin

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
)

func nsAttachNetdev(hostIfName string, containerNsPAth string, ifName string) (*resourcev1beta1.NetworkDeviceData, error) {
	hostDev, err := netlink.LinkByName(hostIfName)
	if err != nil {
		return nil, err
	}

	// Devices can be renamed only when down
	if err = netlink.LinkSetDown(hostDev); err != nil {
		return nil, fmt.Errorf("failed to set %q down: %v", hostDev.Attrs().Name, err)
	}

	// get the existing IP addresses
	addresses, err := netlink.AddrList(hostDev, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("fail to get ip addresses: %w", err)
	}

	containerNs, err := netns.GetFromPath(containerNsPAth)
	if err != nil {
		return nil, err
	}
	defer containerNs.Close()

	attrs := hostDev.Attrs()
	// Store the original name
	attrs.Alias = hostIfName

	// copy from netlink.LinkModify(dev) using only the parts needed
	flags := unix.NLM_F_REQUEST | unix.NLM_F_ACK
	req := nl.NewNetlinkRequest(unix.RTM_NEWLINK, flags)
	// Get a netlink socket in current namespace
	s, err := nl.GetNetlinkSocketAt(netns.None(), netns.None(), unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("could not get network namespace handle: %w", err)
	}
	defer s.Close()

	req.Sockets = map[int]*nl.SocketHandle{
		unix.NETLINK_ROUTE: {Socket: s},
	}

	msg := nl.NewIfInfomsg(unix.AF_UNSPEC)
	msg.Index = int32(attrs.Index)
	req.AddData(msg)

	nameData := nl.NewRtAttr(unix.IFLA_IFNAME, nl.ZeroTerminated(attrs.Name))
	req.AddData(nameData)

	alias := nl.NewRtAttr(unix.IFLA_IFALIAS, []byte(attrs.Alias))
	req.AddData(alias)

	mtu := nl.NewRtAttr(unix.IFLA_MTU, nl.Uint32Attr(uint32(attrs.MTU)))
	req.AddData(mtu)

	val := nl.Uint32Attr(uint32(containerNs))
	attr := nl.NewRtAttr(unix.IFLA_NET_NS_FD, val)
	req.AddData(attr)

	_, err = req.Execute(unix.NETLINK_ROUTE, 0)
	if err != nil {
		return nil, err
	}

	// to avoid golang problem with goroutines we create the socket in the
	// namespace and use it directly
	nhNs, err := netlink.NewHandleAt(containerNs)
	if err != nil {
		return nil, err
	}
	defer nhNs.Close()

	nsLink, err := nhNs.LinkByName(attrs.Name)
	if err != nil {
		return nil, fmt.Errorf("link not found for interface %s on namespace %s: %w", attrs.Name, containerNsPAth, err)
	}
	ifcData := &resourcev1beta1.NetworkDeviceData{
		InterfaceName:   attrs.Name,
		HardwareAddress: nsLink.Attrs().HardwareAddr.String(),
	}

	for _, address := range addresses {
		// remove the interface attribute of the original address
		// to avoid issues when the interface is renamed.
		err = nhNs.AddrAdd(nsLink, &netlink.Addr{IPNet: address.IPNet})
		if err != nil {
			return nil, fmt.Errorf("fail to set up address %s on namespace %s: %w", address.String(), containerNsPAth, err)
		}
		ifcData.IPs = append(ifcData.IPs, address.IPNet.String())
	}
	err = nhNs.LinkSetUp(nsLink)
	if err != nil {
		return nil, fmt.Errorf("failt to set up interface %s on namespace %s: %w", nsLink.Attrs().Name, containerNsPAth, err)
	}

	return ifcData, nil
}

func nsDetachNetdev(containerNsPAth string, devName string) error {
	containerNs, err := netns.GetFromPath(containerNsPAth)
	if err != nil {
		return fmt.Errorf("could not get network namespace from path %s for network device %s : %w", containerNsPAth, devName, err)
	}
	defer containerNs.Close()
	// to avoid golang problem with goroutines we create the socket in the
	// namespace and use it directly
	nhNs, err := netlink.NewHandleAt(containerNs)
	if err != nil {
		return fmt.Errorf("could not get network namespace handle: %w", err)
	}
	defer nhNs.Close()

	nsLink, err := nhNs.LinkByName(devName)
	if err != nil {
		return fmt.Errorf("link not found for interface %s on namespace %s: %w", devName, containerNsPAth, err)
	}

	// set the device down to avoid network conflicts
	// when it is restored to the original namespace
	err = nhNs.LinkSetDown(nsLink)
	if err != nil {
		return err
	}

	// get the existing IP addresses
	addresses, err := nhNs.AddrList(nsLink, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("fail to get ip addresses: %w", err)
	}

	attrs := nsLink.Attrs()
	// restore the original name if it was renamed
	if nsLink.Attrs().Alias != "" {
		attrs.Name = nsLink.Attrs().Alias
	}

	rootNs, err := netns.Get()
	if err != nil {
		return err
	}
	defer rootNs.Close()

	s, err := nl.GetNetlinkSocketAt(containerNs, rootNs, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("could not get network namespace handle: %w", err)
	}
	defer s.Close()

	// copy from netlink.LinkModify(dev) using only the parts needed
	flags := unix.NLM_F_REQUEST | unix.NLM_F_ACK
	req := nl.NewNetlinkRequest(unix.RTM_NEWLINK, flags)
	req.Sockets = map[int]*nl.SocketHandle{
		unix.NETLINK_ROUTE: {Socket: s},
	}
	msg := nl.NewIfInfomsg(unix.AF_UNSPEC)
	msg.Index = int32(attrs.Index)
	req.AddData(msg)

	nameData := nl.NewRtAttr(unix.IFLA_IFNAME, nl.ZeroTerminated(attrs.Name))
	req.AddData(nameData)

	alias := nl.NewRtAttr(unix.IFLA_IFALIAS, []byte(attrs.Alias))
	req.AddData(alias)

	mtu := nl.NewRtAttr(unix.IFLA_MTU, nl.Uint32Attr(uint32(attrs.MTU)))
	req.AddData(mtu)

	val := nl.Uint32Attr(uint32(rootNs))
	attr := nl.NewRtAttr(unix.IFLA_NET_NS_FD, val)
	req.AddData(attr)

	_, err = req.Execute(unix.NETLINK_ROUTE, 0)
	if err != nil {
		return err
	}

	link, err := netlink.LinkByName(devName)
	if err != nil {
		return fmt.Errorf("link not found for interface %s on runtime namespace: %w", devName, err)
	}

	// set the interface down
	err = netlink.LinkSetDown(link)
	if err != nil {
		return fmt.Errorf("fail to set link down: %w", err)
	}

	for _, address := range addresses {
		err = nhNs.AddrAdd(nsLink, &netlink.Addr{IPNet: address.IPNet})
		if err != nil {
			return fmt.Errorf("fail to set up address %s on runtime namespace: %w", address.String(), err)
		}
	}

	return nil
}
