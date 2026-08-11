package bmc

import (
	"fmt"
	"strconv"
	"strings"

	"upi-forge/internal/yamlx"
)

// StorageInventorySchemaVersion은 결과 문서의 스키마 버전입니다.
//
// 1: 기존 셸 구현의 문서. 버전 필드가 없고 IdracAddress, DellSlotType,
// DellControllerId처럼 장비 이름이 키에 들어 있습니다.
// 2: 장비 중립 키(BmcAddress, SlotType, ControllerId)와 이 버전 필드 도입.
const StorageInventorySchemaVersion = 2

// StorageInventory는 스토리지 인벤토리 문서입니다.
type StorageInventory struct {
	SchemaVersion        int                `json:"SchemaVersion"`
	CollectedAt          string             `json:"CollectedAt"`
	BMCAddress           string             `json:"BmcAddress"`
	SystemModel          string             `json:"SystemModel"`
	ServiceTag           string             `json:"ServiceTag"`
	StoragePcieFunctions []PCIeFunction     `json:"StoragePcieFunctions"`
	Storage              []StorageContainer `json:"Storage"`
}

// PCIeFunction은 스토리지 관련 PCIe 함수입니다.
type PCIeFunction struct {
	FunctionID            string `json:"FunctionId"`
	PciAddress            string `json:"PciAddress"`
	PredictedByPathPrefix string `json:"PredictedByPathPrefix"`
	DeviceClass           string `json:"DeviceClass"`
	Name                  string `json:"Name"`
	// SlotType과 ControllerID는 장비 OEM 데이터가 보고하는 값입니다.
	// (Dell: Oem.Dell.DellPCIeFunction의 SlotType, Id)
	SlotType               *string  `json:"SlotType"`
	ControllerID           *string  `json:"ControllerId"`
	StorageControllerLinks []string `json:"StorageControllerLinks"`
	PCIeDeviceURI          string   `json:"PCIeDeviceUri"`
}

// StorageContainer는 스토리지 컨트롤러 하나와 그에 붙은 디스크/볼륨입니다.
type StorageContainer struct {
	ID         string        `json:"Id"`
	Controller Controller    `json:"Controller"`
	Pcie       *PCIeFunction `json:"Pcie"`
	Drives     []Drive       `json:"Drives"`
	Volumes    []Volume      `json:"Volumes"`
}

// Controller는 스토리지 컨트롤러 요약입니다.
type Controller struct {
	Name            string  `json:"Name"`
	Model           *string `json:"Model"`
	FirmwareVersion *string `json:"FirmwareVersion"`
	State           *string `json:"State"`
}

// Drive는 물리 디스크입니다.
type Drive struct {
	ID                string   `json:"Id"`
	Name              string   `json:"Name"`
	Model             string   `json:"Model"`
	Manufacturer      string   `json:"Manufacturer"`
	SerialNumber      string   `json:"SerialNumber"`
	MediaType         string   `json:"MediaType"`
	Protocol          string   `json:"Protocol"`
	CapacityBytes     *int64   `json:"CapacityBytes"`
	CapacityGB        *float64 `json:"CapacityGB"`
	CapacityGiB       *float64 `json:"CapacityGiB"`
	DurableName       *string  `json:"DurableName"`
	DurableNameFormat *string  `json:"DurableNameFormat"`
	SlotOrdinal       *int     `json:"SlotOrdinal"`
	LifeLeftPercent   *int     `json:"LifeLeftPercent"`
	State             *string  `json:"State"`
}

// Volume은 가상 디스크(RAID 볼륨)입니다.
type Volume struct {
	ID            string   `json:"Id"`
	Name          string   `json:"Name"`
	RAIDType      string   `json:"RAIDType"`
	VolumeType    string   `json:"VolumeType"`
	CapacityBytes *int64   `json:"CapacityBytes"`
	CapacityGB    *float64 `json:"CapacityGB"`
	CapacityGiB   *float64 `json:"CapacityGiB"`
	MemberDrives  []string `json:"MemberDrives"`
	State         *string  `json:"State"`
}

// DestDeviceCandidates는 dest-device 후보 요약을 사람이 읽는 형태로 만듭니다.
func (inv *StorageInventory) DestDeviceCandidates() []string {
	var out []string
	for _, container := range inv.Storage {
		byPath := "(PCI 주소 미확인)"
		if container.Pcie != nil && container.Pcie.PredictedByPathPrefix != "" {
			byPath = container.Pcie.PredictedByPathPrefix
		}
		if len(container.Volumes) > 0 {
			for _, volume := range container.Volumes {
				raid := volume.RAIDType
				if raid == "" {
					raid = "none"
				}
				out = append(out, fmt.Sprintf("  [볼륨] %s / %s\n         RAID %s, %s GB\n         by-path 예상: %s-*",
					container.ID, volume.ID, raid, formatCapacity(volume.CapacityGB), byPath))
			}
			continue
		}
		for _, drive := range container.Drives {
			out = append(out, fmt.Sprintf("  [디스크] %s / %s\n         %s, %s GB\n         by-path 예상: %s-*",
				container.ID, drive.ID, orUnknown(drive.Model), formatCapacity(drive.CapacityGB), byPath))
		}
	}
	return out
}

func formatCapacity(v *float64) string {
	if v == nil {
		return "0"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// MarshalYAML은 인벤토리를 YAML 문서로 만듭니다.
//
// NIC 인벤토리와 같은 방식으로 외부 YAML 라이브러리 없이 직접 출력합니다.
// 키 이름은 JSON(스키마 v2)과 동일하며, 두 형식의 스키마는 골든 테스트로
// 고정합니다. 구조체에 필드를 추가할 때는 YAML 출력 코드와 골든 테스트도
// 함께 갱신해야 합니다.
func (inv *StorageInventory) MarshalYAML() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "SchemaVersion: %d\n", inv.SchemaVersion)
	fmt.Fprintf(&b, "CollectedAt: %s\n", yamlx.Quote(inv.CollectedAt))
	fmt.Fprintf(&b, "BmcAddress: %s\n", yamlx.Quote(inv.BMCAddress))
	fmt.Fprintf(&b, "SystemModel: %s\n", yamlx.Quote(inv.SystemModel))
	fmt.Fprintf(&b, "ServiceTag: %s\n", yamlx.Quote(inv.ServiceTag))

	if !writeListHead(&b, "StoragePcieFunctions:", inv.StoragePcieFunctions == nil, len(inv.StoragePcieFunctions)) {
		for i := range inv.StoragePcieFunctions {
			writePCIeFunctionYAML(&b, "  ", &inv.StoragePcieFunctions[i])
		}
	}

	if writeListHead(&b, "Storage:", inv.Storage == nil, len(inv.Storage)) {
		return []byte(b.String())
	}
	for _, container := range inv.Storage {
		fmt.Fprintf(&b, "  - Id: %s\n", yamlx.Quote(container.ID))
		b.WriteString("    Controller:\n")
		fmt.Fprintf(&b, "      Name: %s\n", yamlx.Quote(container.Controller.Name))
		fmt.Fprintf(&b, "      Model: %s\n", quoteOrNull(container.Controller.Model))
		fmt.Fprintf(&b, "      FirmwareVersion: %s\n", quoteOrNull(container.Controller.FirmwareVersion))
		fmt.Fprintf(&b, "      State: %s\n", quoteOrNull(container.Controller.State))
		if container.Pcie == nil {
			b.WriteString("    Pcie: null\n")
		} else {
			b.WriteString("    Pcie:\n")
			writePCIeFunctionMapYAML(&b, "      ", container.Pcie)
		}

		if !writeListHead(&b, "    Drives:", container.Drives == nil, len(container.Drives)) {
			for _, drive := range container.Drives {
				fmt.Fprintf(&b, "      - Id: %s\n", yamlx.Quote(drive.ID))
				fmt.Fprintf(&b, "        Name: %s\n", yamlx.Quote(drive.Name))
				fmt.Fprintf(&b, "        Model: %s\n", yamlx.Quote(drive.Model))
				fmt.Fprintf(&b, "        Manufacturer: %s\n", yamlx.Quote(drive.Manufacturer))
				fmt.Fprintf(&b, "        SerialNumber: %s\n", yamlx.Quote(drive.SerialNumber))
				fmt.Fprintf(&b, "        MediaType: %s\n", yamlx.Quote(drive.MediaType))
				fmt.Fprintf(&b, "        Protocol: %s\n", yamlx.Quote(drive.Protocol))
				fmt.Fprintf(&b, "        CapacityBytes: %s\n", int64OrNull(drive.CapacityBytes))
				fmt.Fprintf(&b, "        CapacityGB: %s\n", floatOrNull(drive.CapacityGB))
				fmt.Fprintf(&b, "        CapacityGiB: %s\n", floatOrNull(drive.CapacityGiB))
				fmt.Fprintf(&b, "        DurableName: %s\n", quoteOrNull(drive.DurableName))
				fmt.Fprintf(&b, "        DurableNameFormat: %s\n", quoteOrNull(drive.DurableNameFormat))
				fmt.Fprintf(&b, "        SlotOrdinal: %s\n", yamlx.Number(drive.SlotOrdinal))
				fmt.Fprintf(&b, "        LifeLeftPercent: %s\n", yamlx.Number(drive.LifeLeftPercent))
				fmt.Fprintf(&b, "        State: %s\n", quoteOrNull(drive.State))
			}
		}

		if writeListHead(&b, "    Volumes:", container.Volumes == nil, len(container.Volumes)) {
			continue
		}
		for _, volume := range container.Volumes {
			fmt.Fprintf(&b, "      - Id: %s\n", yamlx.Quote(volume.ID))
			fmt.Fprintf(&b, "        Name: %s\n", yamlx.Quote(volume.Name))
			fmt.Fprintf(&b, "        RAIDType: %s\n", yamlx.Quote(volume.RAIDType))
			fmt.Fprintf(&b, "        VolumeType: %s\n", yamlx.Quote(volume.VolumeType))
			fmt.Fprintf(&b, "        CapacityBytes: %s\n", int64OrNull(volume.CapacityBytes))
			fmt.Fprintf(&b, "        CapacityGB: %s\n", floatOrNull(volume.CapacityGB))
			fmt.Fprintf(&b, "        CapacityGiB: %s\n", floatOrNull(volume.CapacityGiB))
			if !writeListHead(&b, "        MemberDrives:", volume.MemberDrives == nil, len(volume.MemberDrives)) {
				for _, member := range volume.MemberDrives {
					fmt.Fprintf(&b, "          - %s\n", yamlx.Quote(member))
				}
			}
			fmt.Fprintf(&b, "        State: %s\n", quoteOrNull(volume.State))
		}
	}
	return []byte(b.String())
}

// writePCIeFunctionYAML은 목록 항목("- ") 형태로 PCIe 함수를 출력합니다.
func writePCIeFunctionYAML(b *strings.Builder, indent string, fn *PCIeFunction) {
	fmt.Fprintf(b, "%s- FunctionId: %s\n", indent, yamlx.Quote(fn.FunctionID))
	writePCIeFunctionBodyYAML(b, indent+"  ", fn)
}

// writePCIeFunctionMapYAML은 맵 값 형태로 PCIe 함수를 출력합니다.
func writePCIeFunctionMapYAML(b *strings.Builder, indent string, fn *PCIeFunction) {
	fmt.Fprintf(b, "%sFunctionId: %s\n", indent, yamlx.Quote(fn.FunctionID))
	writePCIeFunctionBodyYAML(b, indent, fn)
}

func writePCIeFunctionBodyYAML(b *strings.Builder, indent string, fn *PCIeFunction) {
	fmt.Fprintf(b, "%sPciAddress: %s\n", indent, yamlx.Quote(fn.PciAddress))
	fmt.Fprintf(b, "%sPredictedByPathPrefix: %s\n", indent, yamlx.Quote(fn.PredictedByPathPrefix))
	fmt.Fprintf(b, "%sDeviceClass: %s\n", indent, yamlx.Quote(fn.DeviceClass))
	fmt.Fprintf(b, "%sName: %s\n", indent, yamlx.Quote(fn.Name))
	fmt.Fprintf(b, "%sSlotType: %s\n", indent, quoteOrNull(fn.SlotType))
	fmt.Fprintf(b, "%sControllerId: %s\n", indent, quoteOrNull(fn.ControllerID))
	if !writeListHead(b, indent+"StorageControllerLinks:", fn.StorageControllerLinks == nil, len(fn.StorageControllerLinks)) {
		for _, link := range fn.StorageControllerLinks {
			fmt.Fprintf(b, "%s  - %s\n", indent, yamlx.Quote(link))
		}
	}
	fmt.Fprintf(b, "%sPCIeDeviceUri: %s\n", indent, yamlx.Quote(fn.PCIeDeviceURI))
}

// writeListHead는 목록 머리를 JSON과 같은 의미로 출력합니다:
// nil은 null, 빈 목록은 [], 항목이 있으면 키만 쓰고 false를 반환합니다.
func writeListHead(b *strings.Builder, key string, isNil bool, n int) bool {
	if isNil {
		b.WriteString(key + " null\n")
		return true
	}
	if n == 0 {
		b.WriteString(key + " []\n")
		return true
	}
	b.WriteString(key + "\n")
	return false
}

func quoteOrNull(v *string) string {
	if v == nil {
		return "null"
	}
	return yamlx.Quote(*v)
}

func int64OrNull(v *int64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatInt(*v, 10)
}

func floatOrNull(v *float64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
