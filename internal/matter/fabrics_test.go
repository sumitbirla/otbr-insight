package matter

import (
	"testing"

	"github.com/otbr-insight/otbr-insight/internal/mdns"
	"github.com/otbr-insight/otbr-insight/internal/model"
)

func TestFabricsGroupsAndFlagsMeshNodes(t *testing.T) {
	instances := []mdns.Instance{
		{Name: "8899AABBCCDDEEFF-00000000AABBCCDD", Host: "0102030405060708.local.", Port: 5540},
		{Name: "1122334455667788-000000000001B669", Host: "bc2411aabbcc.local.", Port: 40130},
		{Name: "1122334455667788-0000000000000014", Host: "0102030405060708.local.", Port: 5540},
		{Name: "1122334455667788-0000000000000003", Host: "0c4ea0aabbcc.local.", Port: 5540, Addresses: []string{"192.168.1.10"}},
		{Name: "1122334455667788-0000000000000019", Host: "0f0e0d0c0b0a0908.local.", Port: 5540},
		{Name: "not-a-matter-name", Host: "x.local."},
	}
	port := 5540
	devices := []model.Device{
		{ExtendedAddress: "0102030405060708"},
		// Registered with SRP but absent from the browse (the truncated-reply case
		// on the border router): still a node, on this mesh.
		{ExtendedAddress: "0203040506070809", OMRIPv6Address: "fd11:2233:4455:1::9", Services: []model.AdvertisedService{
			{Instance: "1122334455667788-0000000000000012", Type: "_matter._tcp", Port: &port},
			{Instance: "8899AABBCCDDEEFF-0000000011223344", Type: "_matter._tcp", Port: &port},
			{Instance: "0F0E0D0C0B0A0908", Type: "_matterc._udp", Port: &port},
		}},
	}
	fabrics := Fabrics(instances, devices)
	if len(fabrics) != 2 {
		t.Fatalf("fabrics = %+v, want 2", fabrics)
	}
	// The fabric with a mesh device leads; the other has one too but is sorted by ID after.
	first := fabrics[0]
	if first.ID != "1122334455667788" || first.NodeCount != 5 || first.MeshCount != 2 {
		t.Fatalf("first fabric = %+v", first)
	}
	if !first.Nodes[0].OnMesh || first.Nodes[0].NodeID != "0x12" || first.Nodes[0].Addresses[0] != "fd11:2233:4455:1::9" {
		t.Errorf("SRP-only mesh node should lead with its OMR address: %+v", first.Nodes[0])
	}
	if !first.Nodes[1].OnMesh || first.Nodes[1].NodeID != "0x14" || first.Nodes[1].ExtendedAddress != "0102030405060708" {
		t.Errorf("browsed mesh node next: %+v", first.Nodes[1])
	}
	// A Thread-shaped hostname that is not in the inventory is linked but not on this mesh.
	if first.Nodes[4].NodeID != "0x1B669" {
		t.Errorf("numeric node order: %+v", first.Nodes)
	}
	if fabrics[1].NodeCount != 2 || fabrics[1].MeshCount != 2 {
		t.Errorf("second fabric = %+v, want the browsed and the SRP-only mesh nodes", fabrics[1])
	}
	var stranger model.MatterNode
	for _, n := range first.Nodes {
		if n.NodeID == "0x19" {
			stranger = n
		}
	}
	if stranger.ExtendedAddress != "0f0e0d0c0b0a0908" || stranger.OnMesh {
		t.Errorf("unknown Thread host = %+v, want linked but off-mesh", stranger)
	}
	if fabrics[1].Nodes[0].Port == nil || *fabrics[1].Nodes[0].Port != 5540 {
		t.Errorf("port lost: %+v", fabrics[1].Nodes[0])
	}
}

func TestFabricsDeduplicatesBrowseAgainstRegistry(t *testing.T) {
	port := 5540
	devices := []model.Device{{ExtendedAddress: "0102030405060708", Services: []model.AdvertisedService{
		{Instance: "1122334455667788-0000000000000014", Type: "_matter._tcp", Port: &port}}}}
	instances := []mdns.Instance{{Name: "1122334455667788-0000000000000014", Host: "0102030405060708.local.", Port: 5540, Addresses: []string{"fd11:2233:4455:1::8"}}}
	fabrics := Fabrics(instances, devices)
	if len(fabrics) != 1 || fabrics[0].NodeCount != 1 {
		t.Fatalf("fabrics = %+v, want one node seen once", fabrics)
	}
}
