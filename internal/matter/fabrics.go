// Package matter shapes what mDNS says about Matter nodes into fabrics.
//
// A fabric is a controller's trust domain, not a Thread concept: it spans every
// device that controller commissioned over any link. The mesh reflects fabrics
// only through its devices, so with an empty mesh the LAN is the only place to
// see which fabrics exist at all.
package matter

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/otbr-insight/otbr-insight/internal/mdns"
	"github.com/otbr-insight/otbr-insight/internal/model"
)

// OperationalService is the mDNS service type Matter nodes advertise once
// commissioned. Instance names are "<compressed-fabric-id>-<node-id>".
const OperationalService = "_matter._tcp"

// Fabrics groups nodes by fabric from two sources: the mDNS browse for the LAN,
// and each mesh device's own SRP registration for the mesh. Nodes whose
// hostname is a device on the mesh are flagged and linked by extended address;
// those come first in each fabric, followed by the rest in node-ID order.
// Fabrics are ordered by how many mesh devices they hold, then by ID, so the
// ones relevant here lead.
//
// The SRP source is not redundant. Measured on the border router: otbr-agent's
// own multicast replies never reach a local socket, so a browse run there only
// gets its single legacy unicast reply, which is truncated to one packet and
// silently drops the last Thread devices. The registry is authoritative for
// what the mesh advertises, so it is merged first and mDNS fills in the rest.
func Fabrics(instances []mdns.Instance, devices []model.Device) []model.MatterFabric {
	onMesh := map[string]bool{}
	byFabric := map[string]*model.MatterFabric{}
	seen := map[string]bool{}
	add := func(fabric string, n model.MatterNode) {
		key := fabric + "-" + n.NodeID
		if seen[key] {
			return
		}
		seen[key] = true
		entry := byFabric[fabric]
		if entry == nil {
			entry = &model.MatterFabric{ID: fabric}
			byFabric[fabric] = entry
		}
		entry.Nodes = append(entry.Nodes, n)
	}
	for _, device := range devices {
		ext := strings.ToLower(device.ExtendedAddress)
		if ext == "" {
			continue
		}
		onMesh[ext] = true
		for _, service := range device.Services {
			if service.Type != OperationalService {
				continue
			}
			fabric, node, ok := Identity(service.Instance)
			if !ok {
				continue
			}
			n := model.MatterNode{NodeID: node, Host: ext + ".local.", Port: service.Port, ExtendedAddress: ext, OnMesh: true}
			if device.OMRIPv6Address != "" {
				n.Addresses = []string{device.OMRIPv6Address}
			}
			add(fabric, n)
		}
	}
	for _, instance := range instances {
		fabric, node, ok := Identity(instance.Name)
		if !ok {
			continue
		}
		n := model.MatterNode{NodeID: node, Host: instance.Host, Addresses: instance.Addresses}
		if instance.Port > 0 {
			port := instance.Port
			n.Port = &port
		}
		if ext, ok := hostExtendedAddress(instance.Host); ok {
			n.ExtendedAddress = ext
			n.OnMesh = onMesh[ext]
		}
		add(fabric, n)
	}
	fabrics := make([]model.MatterFabric, 0, len(byFabric))
	for _, entry := range byFabric {
		sort.SliceStable(entry.Nodes, func(i, j int) bool {
			a, b := entry.Nodes[i], entry.Nodes[j]
			if a.OnMesh != b.OnMesh {
				return a.OnMesh
			}
			return nodeOrder(a.NodeID) < nodeOrder(b.NodeID)
		})
		entry.NodeCount = len(entry.Nodes)
		for _, n := range entry.Nodes {
			if n.OnMesh {
				entry.MeshCount++
			}
		}
		fabrics = append(fabrics, *entry)
	}
	sort.SliceStable(fabrics, func(i, j int) bool {
		if fabrics[i].MeshCount != fabrics[j].MeshCount {
			return fabrics[i].MeshCount > fabrics[j].MeshCount
		}
		return fabrics[i].ID < fabrics[j].ID
	})
	return fabrics
}

// Identity decodes "<fabric>-<node>" (two 16-digit hex fields) into the fabric
// ID as printed and the node ID as a short "0x…" literal, the way controllers
// show it.
func Identity(instance string) (fabric, node string, ok bool) {
	fabric, node, ok = strings.Cut(strings.TrimSpace(instance), "-")
	if !ok || len(fabric) != 16 || len(node) != 16 {
		return "", "", false
	}
	if _, err := hex.DecodeString(fabric); err != nil {
		return "", "", false
	}
	if _, err := hex.DecodeString(node); err != nil {
		return "", "", false
	}
	short := strings.TrimLeft(strings.ToUpper(node), "0")
	if short == "" {
		short = "0"
	}
	return strings.ToUpper(fabric), "0x" + short, true
}

// hostExtendedAddress recognises the "<extaddr>.local." hostnames the border
// router's advertising proxy uses for Thread devices.
func hostExtendedAddress(host string) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(host), ".local.")
	if len(name) != 16 {
		return "", false
	}
	if _, err := hex.DecodeString(name); err != nil {
		return "", false
	}
	return name, true
}

// nodeOrder sorts "0x14" numerically rather than lexically, padding to the
// full width so "0x1B669" follows "0x19".
func nodeOrder(node string) string {
	digits := strings.TrimPrefix(node, "0x")
	return strings.Repeat("0", 16-len(digits)) + digits
}
