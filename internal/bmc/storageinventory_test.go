package bmc_test

// storage-inventory.json의 공개 스키마를 고정합니다.
//
// 스키마 v2는 기존 셸 구현(v1)의 장비 이름 키를 중립화한 것입니다:
// IdracAddress -> BmcAddress, DellSlotType -> SlotType,
// DellControllerId -> ControllerId. 그리고 SchemaVersion 필드를 도입했습니다.
// 이 파일을 읽는 자동화는 키 이름에 의존하므로, 키가 바뀌면
// 골든 테스트가 실패해 의도하지 않은 스키마 변경을 막습니다.

import (
	"encoding/json"
	"strings"
	"testing"

	"upi-forge/internal/bmc"
)

func strPtr(v string) *string { return &v }

func int64Ptr(v int64) *int64 { return &v }

func float64Ptr(v float64) *float64 { return &v }

func sampleStorageInventory() *bmc.StorageInventory {
	pcie := bmc.PCIeFunction{
		FunctionID:             "0-60-0-0",
		PciAddress:             "0000:3c:00.0",
		PredictedByPathPrefix:  "/dev/disk/by-path/pci-0000:3c:00.0",
		DeviceClass:            "NonVolatileMemoryController",
		Name:                   "PCIe SSD",
		SlotType:               strPtr("FullLength"),
		ControllerID:           strPtr("Disk.Bay.23"),
		StorageControllerLinks: []string{"/redfish/v1/Systems/System.Embedded.1/Storage/CPU.1"},
		PCIeDeviceURI:          "/redfish/v1/Chassis/System.Embedded.1/PCIeDevices/60-0",
	}
	return &bmc.StorageInventory{
		SchemaVersion:        bmc.StorageInventorySchemaVersion,
		CollectedAt:          "2026-01-02T03:04:05Z",
		BMCAddress:           "192.0.2.200",
		SystemModel:          "Example R100",
		ServiceTag:           "ABC1234",
		StoragePcieFunctions: []bmc.PCIeFunction{pcie},
		Storage: []bmc.StorageContainer{{
			ID: "CPU.1",
			Controller: bmc.Controller{
				Name:  "PCIe SSD Controller",
				State: strPtr("Enabled"),
			},
			Pcie: &pcie,
			Drives: []bmc.Drive{{
				ID:            "Disk.Bay.23",
				Name:          "PCIe SSD in Slot 23",
				Model:         "Example NVMe 1.6TB",
				CapacityBytes: int64Ptr(1_600_000_000_000),
				CapacityGB:    float64Ptr(1600),
				CapacityGiB:   float64Ptr(1490.1),
			}},
			Volumes: []bmc.Volume{},
		}},
	}
}

// 스키마 v2의 JSON 골든 테스트입니다. 키 이름(중립화된 BmcAddress,
// SlotType, ControllerId 포함)과 SchemaVersion 값이 그대로 고정됩니다.
func TestStorageInventoryJSONGolden(t *testing.T) {
	if bmc.StorageInventorySchemaVersion != 2 {
		t.Fatalf("스키마 버전을 바꿨다면 이 골든 테스트와 문서를 함께 갱신하세요: %d",
			bmc.StorageInventorySchemaVersion)
	}

	data, err := json.MarshalIndent(sampleStorageInventory(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	want := `{
  "SchemaVersion": 2,
  "CollectedAt": "2026-01-02T03:04:05Z",
  "BmcAddress": "192.0.2.200",
  "SystemModel": "Example R100",
  "ServiceTag": "ABC1234",
  "StoragePcieFunctions": [
    {
      "FunctionId": "0-60-0-0",
      "PciAddress": "0000:3c:00.0",
      "PredictedByPathPrefix": "/dev/disk/by-path/pci-0000:3c:00.0",
      "DeviceClass": "NonVolatileMemoryController",
      "Name": "PCIe SSD",
      "SlotType": "FullLength",
      "ControllerId": "Disk.Bay.23",
      "StorageControllerLinks": [
        "/redfish/v1/Systems/System.Embedded.1/Storage/CPU.1"
      ],
      "PCIeDeviceUri": "/redfish/v1/Chassis/System.Embedded.1/PCIeDevices/60-0"
    }
  ],
  "Storage": [
    {
      "Id": "CPU.1",
      "Controller": {
        "Name": "PCIe SSD Controller",
        "Model": null,
        "FirmwareVersion": null,
        "State": "Enabled"
      },
      "Pcie": {
        "FunctionId": "0-60-0-0",
        "PciAddress": "0000:3c:00.0",
        "PredictedByPathPrefix": "/dev/disk/by-path/pci-0000:3c:00.0",
        "DeviceClass": "NonVolatileMemoryController",
        "Name": "PCIe SSD",
        "SlotType": "FullLength",
        "ControllerId": "Disk.Bay.23",
        "StorageControllerLinks": [
          "/redfish/v1/Systems/System.Embedded.1/Storage/CPU.1"
        ],
        "PCIeDeviceUri": "/redfish/v1/Chassis/System.Embedded.1/PCIeDevices/60-0"
      },
      "Drives": [
        {
          "Id": "Disk.Bay.23",
          "Name": "PCIe SSD in Slot 23",
          "Model": "Example NVMe 1.6TB",
          "Manufacturer": "",
          "SerialNumber": "",
          "MediaType": "",
          "Protocol": "",
          "CapacityBytes": 1600000000000,
          "CapacityGB": 1600,
          "CapacityGiB": 1490.1,
          "DurableName": null,
          "DurableNameFormat": null,
          "SlotOrdinal": null,
          "LifeLeftPercent": null,
          "State": null
        }
      ],
      "Volumes": []
    }
  ]
}`
	if got != want {
		t.Errorf("JSON 출력이 스키마 v2 골든과 다릅니다:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// v1의 장비 이름 키가 되살아나지 않아야 합니다.
	for _, old := range []string{"IdracAddress", "DellSlotType", "DellControllerId"} {
		if strings.Contains(got, old) {
			t.Errorf("v1 키 %s가 출력에 있으면 안 됩니다", old)
		}
	}
}

// YAML 출력도 JSON과 같은 키(스키마 v2)로 고정합니다.
func TestStorageInventoryMarshalYAMLGolden(t *testing.T) {
	want := `SchemaVersion: 2
CollectedAt: "2026-01-02T03:04:05Z"
BmcAddress: "192.0.2.200"
SystemModel: "Example R100"
ServiceTag: "ABC1234"
StoragePcieFunctions:
  - FunctionId: "0-60-0-0"
    PciAddress: "0000:3c:00.0"
    PredictedByPathPrefix: "/dev/disk/by-path/pci-0000:3c:00.0"
    DeviceClass: "NonVolatileMemoryController"
    Name: "PCIe SSD"
    SlotType: "FullLength"
    ControllerId: "Disk.Bay.23"
    StorageControllerLinks:
      - "/redfish/v1/Systems/System.Embedded.1/Storage/CPU.1"
    PCIeDeviceUri: "/redfish/v1/Chassis/System.Embedded.1/PCIeDevices/60-0"
Storage:
  - Id: "CPU.1"
    Controller:
      Name: "PCIe SSD Controller"
      Model: null
      FirmwareVersion: null
      State: "Enabled"
    Pcie:
      FunctionId: "0-60-0-0"
      PciAddress: "0000:3c:00.0"
      PredictedByPathPrefix: "/dev/disk/by-path/pci-0000:3c:00.0"
      DeviceClass: "NonVolatileMemoryController"
      Name: "PCIe SSD"
      SlotType: "FullLength"
      ControllerId: "Disk.Bay.23"
      StorageControllerLinks:
        - "/redfish/v1/Systems/System.Embedded.1/Storage/CPU.1"
      PCIeDeviceUri: "/redfish/v1/Chassis/System.Embedded.1/PCIeDevices/60-0"
    Drives:
      - Id: "Disk.Bay.23"
        Name: "PCIe SSD in Slot 23"
        Model: "Example NVMe 1.6TB"
        Manufacturer: ""
        SerialNumber: ""
        MediaType: ""
        Protocol: ""
        CapacityBytes: 1600000000000
        CapacityGB: 1600
        CapacityGiB: 1490.1
        DurableName: null
        DurableNameFormat: null
        SlotOrdinal: null
        LifeLeftPercent: null
        State: null
    Volumes: []
`
	got := string(sampleStorageInventory().MarshalYAML())
	if got != want {
		t.Errorf("YAML 출력이 고정 형식과 다릅니다:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// 볼륨과 목록 표기도 JSON과 같은 의미로 출력되어야 합니다:
// nil 목록은 null, 실제 빈 목록은 [] (encoding/json과 동일 규칙).
func TestStorageInventoryMarshalYAMLVolumesAndEmpty(t *testing.T) {
	inv := sampleStorageInventory()
	inv.StoragePcieFunctions = []bmc.PCIeFunction{} // 빈 목록 -> []
	inv.Storage[0].Pcie = nil
	inv.Storage[0].Drives = nil // nil -> null (JSON도 null)
	inv.Storage[0].Volumes = []bmc.Volume{{
		ID: "Disk.Virtual.0", RAIDType: "RAID1",
		MemberDrives: []string{"Disk.Bay.0", "Disk.Bay.1"},
	}}
	got := string(inv.MarshalYAML())

	for _, want := range []string{
		"StoragePcieFunctions: []\n",
		"    Pcie: null\n",
		"    Drives: null\n",
		"      - Id: \"Disk.Virtual.0\"\n",
		"        MemberDrives:\n          - \"Disk.Bay.0\"\n          - \"Disk.Bay.1\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("YAML에 %q가 있어야 합니다:\n%s", want, got)
		}
	}
}

func TestDestDeviceCandidates(t *testing.T) {
	inv := sampleStorageInventory()

	// 볼륨이 없으면 디스크 단위로 후보를 냅니다.
	lines := inv.DestDeviceCandidates()
	if len(lines) != 1 || !strings.Contains(lines[0], "[디스크]") {
		t.Fatalf("디스크 후보 한 줄이 나와야 합니다: %v", lines)
	}
	if !strings.Contains(lines[0], "/dev/disk/by-path/pci-0000:3c:00.0-*") {
		t.Errorf("by-path 예상 접두사가 있어야 합니다: %s", lines[0])
	}
	if !strings.Contains(lines[0], "1600 GB") {
		t.Errorf("용량이 있어야 합니다: %s", lines[0])
	}

	// 볼륨이 있으면 디스크 대신 볼륨 단위로 후보를 냅니다.
	inv.Storage[0].Volumes = []bmc.Volume{{
		ID:         "Disk.Virtual.0",
		RAIDType:   "RAID1",
		CapacityGB: float64Ptr(479.6),
	}}
	lines = inv.DestDeviceCandidates()
	if len(lines) != 1 || !strings.Contains(lines[0], "[볼륨]") {
		t.Fatalf("볼륨 후보 한 줄이 나와야 합니다: %v", lines)
	}
	if !strings.Contains(lines[0], "RAID RAID1") || !strings.Contains(lines[0], "479.6 GB") {
		t.Errorf("RAID 종류와 용량이 있어야 합니다: %s", lines[0])
	}

	// PCIe 대응을 찾지 못한 컨트롤러는 미확인으로 표시합니다.
	inv.Storage[0].Pcie = nil
	lines = inv.DestDeviceCandidates()
	if !strings.Contains(lines[0], "(PCI 주소 미확인)") {
		t.Errorf("PCIe 미확인 표시가 있어야 합니다: %s", lines[0])
	}

	// PCIe는 찾았지만 주소 해석에 실패한 경우도 미확인으로 표시합니다.
	inv.Storage[0].Pcie = &bmc.PCIeFunction{FunctionID: "unparsable"}
	lines = inv.DestDeviceCandidates()
	if !strings.Contains(lines[0], "(PCI 주소 미확인)") {
		t.Errorf("주소 없는 PCIe도 미확인으로 표시해야 합니다: %s", lines[0])
	}
}
