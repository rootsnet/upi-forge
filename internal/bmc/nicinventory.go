package bmc

import (
	"fmt"
	"strings"

	"upi-forge/internal/yamlx"
)

// NICInventorySchemaVersion은 결과 문서의 스키마 버전입니다.
const NICInventorySchemaVersion = 1

// NICInventory는 OCP 설치 판단용 NIC 인벤토리 데이터입니다. YAML과 JSON
// 출력이 이 구조체를 공통으로 사용하며, 두 형식의 스키마는 골든 테스트로
// 고정합니다.
type NICInventory struct {
	SchemaVersion  int          `json:"schema_version"`
	Purpose        string       `json:"purpose"`
	CollectedAtUTC string       `json:"collected_at_utc"`
	System         NICSystem    `json:"system"`
	Adapters       []NICAdapter `json:"network_adapters"`
}

// NICSystem은 서버 식별 정보입니다.
type NICSystem struct {
	ID           string `json:"id"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	ServiceTag   string `json:"service_tag"`
	SerialNumber string `json:"serial_number"`
	UUID         string `json:"uuid"`
}

// NICAdapter는 NIC 카드 하나입니다.
type NICAdapter struct {
	ID           string    `json:"id"`
	Slot         *int      `json:"slot"`
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	PartNumber   string    `json:"part_number"`
	SerialNumber string    `json:"serial_number"`
	Firmware     string    `json:"firmware"`
	Health       string    `json:"health"`
	Ports        []NICPort `json:"ports"`
}

// NICPort는 물리 포트 하나입니다.
type NICPort struct {
	ID                  string `json:"id"`
	FunctionID          string `json:"function_id"`
	PhysicalPort        *int   `json:"physical_port"`
	Partition           *int   `json:"partition"`
	PermanentMACAddress string `json:"permanent_mac_address"`
	CurrentMACAddress   string `json:"current_mac_address"`
	LinkStatus          string `json:"link_status"`
	CurrentSpeedMbps    *int   `json:"current_speed_mbps"`
	Health              string `json:"health"`
}

// PortCount는 수집한 전체 포트 수입니다.
func (inv *NICInventory) PortCount() int {
	n := 0
	for _, adapter := range inv.Adapters {
		n += len(adapter.Ports)
	}
	return n
}

// Validate는 포트와 영구 MAC 주소가 빠짐없이 수집됐는지 확인합니다.
func (inv *NICInventory) Validate() error {
	if inv.PortCount() == 0 {
		return fmt.Errorf("포트를 수집하지 못했습니다")
	}
	empty := 0
	for _, adapter := range inv.Adapters {
		for _, port := range adapter.Ports {
			if port.PermanentMACAddress == "" {
				empty++
			}
		}
	}
	if empty > 0 {
		return fmt.Errorf("영구 MAC이 없는 포트가 %d개입니다", empty)
	}
	return nil
}

// MarshalYAML은 인벤토리를 YAML 문서로 만듭니다.
//
// 외부 YAML 라이브러리에 의존하지 않기 위해 고정된 스키마를 직접 출력합니다.
// JSON과 동일한 구조체를 바탕으로 생성하며, 두 형식의 스키마는 골든 테스트로
// 고정합니다.
func (inv *NICInventory) MarshalYAML() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "schema_version: %d\n", inv.SchemaVersion)
	fmt.Fprintf(&b, "purpose: %s\n", yamlx.Quote(inv.Purpose))
	fmt.Fprintf(&b, "collected_at_utc: %s\n", yamlx.Quote(inv.CollectedAtUTC))
	b.WriteString("system:\n")
	fmt.Fprintf(&b, "  id: %s\n", yamlx.Quote(inv.System.ID))
	fmt.Fprintf(&b, "  manufacturer: %s\n", yamlx.Quote(inv.System.Manufacturer))
	fmt.Fprintf(&b, "  model: %s\n", yamlx.Quote(inv.System.Model))
	fmt.Fprintf(&b, "  service_tag: %s\n", yamlx.Quote(inv.System.ServiceTag))
	fmt.Fprintf(&b, "  serial_number: %s\n", yamlx.Quote(inv.System.SerialNumber))
	fmt.Fprintf(&b, "  uuid: %s\n", yamlx.Quote(inv.System.UUID))
	b.WriteString("network_adapters:\n")

	for _, adapter := range inv.Adapters {
		fmt.Fprintf(&b, "  - id: %s\n", yamlx.Quote(adapter.ID))
		fmt.Fprintf(&b, "    slot: %s\n", yamlx.Number(adapter.Slot))
		fmt.Fprintf(&b, "    manufacturer: %s\n", yamlx.Quote(adapter.Manufacturer))
		fmt.Fprintf(&b, "    model: %s\n", yamlx.Quote(adapter.Model))
		fmt.Fprintf(&b, "    part_number: %s\n", yamlx.Quote(adapter.PartNumber))
		fmt.Fprintf(&b, "    serial_number: %s\n", yamlx.Quote(adapter.SerialNumber))
		fmt.Fprintf(&b, "    firmware: %s\n", yamlx.Quote(adapter.Firmware))
		fmt.Fprintf(&b, "    health: %s\n", yamlx.Quote(adapter.Health))
		if len(adapter.Ports) == 0 {
			b.WriteString("    ports: []\n")
			continue
		}
		b.WriteString("    ports:\n")
		for _, port := range adapter.Ports {
			fmt.Fprintf(&b, "      - id: %s\n", yamlx.Quote(port.ID))
			fmt.Fprintf(&b, "        function_id: %s\n", yamlx.Quote(port.FunctionID))
			fmt.Fprintf(&b, "        physical_port: %s\n", yamlx.Number(port.PhysicalPort))
			fmt.Fprintf(&b, "        partition: %s\n", yamlx.Number(port.Partition))
			fmt.Fprintf(&b, "        permanent_mac_address: %s\n", yamlx.Quote(port.PermanentMACAddress))
			fmt.Fprintf(&b, "        current_mac_address: %s\n", yamlx.Quote(port.CurrentMACAddress))
			fmt.Fprintf(&b, "        link_status: %s\n", yamlx.Quote(port.LinkStatus))
			fmt.Fprintf(&b, "        current_speed_mbps: %s\n", yamlx.Number(port.CurrentSpeedMbps))
			fmt.Fprintf(&b, "        health: %s\n", yamlx.Quote(port.Health))
		}
	}
	return []byte(b.String())
}
