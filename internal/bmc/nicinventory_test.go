package bmc_test

import (
	"encoding/json"
	"strings"
	"testing"

	"upi-forge/internal/bmc"
)

func intPtr(v int) *int { return &v }

func sampleNICInventory() *bmc.NICInventory {
	return &bmc.NICInventory{
		SchemaVersion:  bmc.NICInventorySchemaVersion,
		Purpose:        "ocp-install-nic-inventory",
		CollectedAtUTC: "2026-01-02T03:04:05Z",
		System: bmc.NICSystem{
			ID:           "System.Embedded.1",
			Manufacturer: "Example Inc.",
			Model:        "Example R100",
			ServiceTag:   "ABC1234",
			SerialNumber: "SN0001",
			UUID:         "00000000-0000-0000-0000-000000000001",
		},
		Adapters: []bmc.NICAdapter{{
			ID:           "NIC.Slot.2",
			Slot:         intPtr(2),
			Manufacturer: "Example Inc.",
			Model:        "Example 25G 2P",
			PartNumber:   "P0001",
			SerialNumber: "SN0002",
			Firmware:     "1.0.0",
			Health:       "OK",
			Ports: []bmc.NICPort{{
				ID:                  "NIC.Slot.2-1",
				FunctionID:          "NIC.Slot.2-1-1",
				PhysicalPort:        intPtr(1),
				Partition:           intPtr(1),
				PermanentMACAddress: "00:00:5E:00:53:01",
				CurrentMACAddress:   "00:00:5E:00:53:01",
				LinkStatus:          "Up",
				CurrentSpeedMbps:    intPtr(25000),
				Health:              "OK",
			}},
		}},
	}
}

// 운영자가 읽고 다른 도구가 해석하는 YAML 출력 형식을 그대로 고정합니다.
// 키 이름, 순서, 들여쓰기가 바뀌면 실패합니다.
func TestNICInventoryMarshalYAMLGolden(t *testing.T) {
	want := `schema_version: 1
purpose: "ocp-install-nic-inventory"
collected_at_utc: "2026-01-02T03:04:05Z"
system:
  id: "System.Embedded.1"
  manufacturer: "Example Inc."
  model: "Example R100"
  service_tag: "ABC1234"
  serial_number: "SN0001"
  uuid: "00000000-0000-0000-0000-000000000001"
network_adapters:
  - id: "NIC.Slot.2"
    slot: 2
    manufacturer: "Example Inc."
    model: "Example 25G 2P"
    part_number: "P0001"
    serial_number: "SN0002"
    firmware: "1.0.0"
    health: "OK"
    ports:
      - id: "NIC.Slot.2-1"
        function_id: "NIC.Slot.2-1-1"
        physical_port: 1
        partition: 1
        permanent_mac_address: "00:00:5E:00:53:01"
        current_mac_address: "00:00:5E:00:53:01"
        link_status: "Up"
        current_speed_mbps: 25000
        health: "OK"
`
	got := string(sampleNICInventory().MarshalYAML())
	if got != want {
		t.Errorf("YAML 출력이 고정 형식과 다릅니다:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// 포트가 없는 어댑터는 빈 목록으로 명시되어야 합니다.
func TestNICInventoryMarshalYAMLEmptyPorts(t *testing.T) {
	inv := sampleNICInventory()
	inv.Adapters[0].Ports = nil
	if !strings.Contains(string(inv.MarshalYAML()), "    ports: []\n") {
		t.Error("포트가 없으면 ports: []가 출력되어야 합니다")
	}
}

// JSON 키 이름을 고정합니다. YAML과 같은 구조체에서 나오므로
// 두 형식의 키가 함께 검증됩니다.
func TestNICInventoryJSONKeys(t *testing.T) {
	data, err := json.Marshal(sampleNICInventory())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"schema_version":1`, `"purpose"`, `"collected_at_utc"`, `"system"`,
		`"service_tag"`, `"network_adapters"`, `"ports"`,
		`"permanent_mac_address"`, `"current_speed_mbps"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON에 %s 키가 있어야 합니다:\n%s", key, data)
		}
	}
}

func TestNICInventoryValidate(t *testing.T) {
	if err := sampleNICInventory().Validate(); err != nil {
		t.Errorf("정상 인벤토리는 통과해야 합니다: %v", err)
	}

	empty := &bmc.NICInventory{}
	if err := empty.Validate(); err == nil {
		t.Error("포트가 없으면 실패해야 합니다")
	}

	noMAC := sampleNICInventory()
	noMAC.Adapters[0].Ports[0].PermanentMACAddress = ""
	if err := noMAC.Validate(); err == nil {
		t.Error("영구 MAC이 없는 포트가 있으면 실패해야 합니다")
	}
}

func TestNICInventoryPortCount(t *testing.T) {
	inv := sampleNICInventory()
	inv.Adapters = append(inv.Adapters, bmc.NICAdapter{
		Ports: []bmc.NICPort{{}, {}},
	})
	if got := inv.PortCount(); got != 3 {
		t.Errorf("PortCount: got=%d want=3", got)
	}
}
