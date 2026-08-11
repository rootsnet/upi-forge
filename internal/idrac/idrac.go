// Package idrac는 Dell PowerEdge의 iDRAC10에서 검증한 Redfish 작업을
// 구현합니다. 다음 동작은 iDRAC10의 특성에 맞춰 처리합니다.
//
//   - BootOrder는 Systems 리소스에 직접 PATCH할 수 없고 Settings 리소스에만
//     쓸 수 있습니다(Base.1.18.PropertyNotWritable).
//   - 원타임 가상 미디어 부팅은 표준 BootSourceOverrideTarget이 아니라
//     Dell 전용 속성 세 개(ServerBoot.1.FirstBootDevice, ServerBoot.1.BootOnce,
//     VirtualMedia.<N>.BootOnce)로 설정해야 합니다.
//
// bmc 패키지가 정의하는 장비 중립 계층의 첫 드라이버이며, 요청과 인벤토리
// 결과는 bmc의 타입을 사용합니다.
package idrac

import (
	"context"
	"fmt"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/redfish"
)

// Driver는 bmc 중립 계층에 연결하는 iDRAC10 드라이버입니다.
// cli는 pathset의 bmc.type이 idrac10일 때 이 드라이버를 사용합니다.
func Driver() *bmc.Driver {
	return &bmc.Driver{
		Boot:             Boot,
		Eject:            Eject,
		NICInventory:     CollectNICInventory,
		StorageInventory: CollectStorageInventory,
		ConsoleName:      "iDRAC Virtual Console",
		EjectHint:        "iDRAC 속성 ServerBoot.1.FirstBootDevice가 Normal인지도 확인하세요.",
	}
}

// iDRAC10에서 사용하는 Redfish 리소스 경로입니다.
const (
	SystemPath         = "/redfish/v1/Systems/System.Embedded.1"
	SystemSettingsPath = SystemPath + "/Settings"
	ChassisPath        = "/redfish/v1/Chassis/System.Embedded.1"
	AttributesPath     = "/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellAttributes/iDRAC.Embedded.1"
)

// sleep은 컨텍스트가 취소되면 즉시 끝나는 대기입니다.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// System은 ComputerSystem 리소스에서 사용하는 필드입니다.
type System struct {
	ID           string         `json:"Id"`
	Manufacturer string         `json:"Manufacturer"`
	Model        string         `json:"Model"`
	SerialNumber string         `json:"SerialNumber"`
	SKU          string         `json:"SKU"`
	UUID         string         `json:"UUID"`
	BiosVersion  string         `json:"BiosVersion"`
	PowerState   string         `json:"PowerState"`
	Status       redfish.Status `json:"Status"`
	Boot         struct {
		BootOrder                 []string `json:"BootOrder"`
		BootSourceOverrideEnabled string   `json:"BootSourceOverrideEnabled"`
		BootSourceOverrideTarget  string   `json:"BootSourceOverrideTarget"`
		BootSourceOverrideMode    string   `json:"BootSourceOverrideMode"`
	} `json:"Boot"`
	VirtualMedia       redfish.Ref `json:"VirtualMedia"`
	EthernetInterfaces redfish.Ref `json:"EthernetInterfaces"`
	Storage            redfish.Ref `json:"Storage"`
}

// GetSystem은 System.Embedded.1 리소스를 읽습니다.
func GetSystem(ctx context.Context, c *redfish.Client) (*System, error) {
	var sys System
	if err := c.Get(ctx, SystemPath, &sys); err != nil {
		return nil, err
	}
	return &sys, nil
}

// Reset은 ComputerSystem.Reset 액션을 호출합니다.
func Reset(ctx context.Context, c *redfish.Client, resetType string) error {
	_, err := c.Post(ctx, SystemPath+"/Actions/ComputerSystem.Reset",
		map[string]string{"ResetType": resetType})
	if err != nil {
		return fmt.Errorf("전원 제어(%s)에 실패했습니다: %w", resetType, err)
	}
	return nil
}

// Attributes는 DellAttributes 리소스입니다.
type Attributes struct {
	Attributes map[string]any `json:"Attributes"`
}

// GetAttributes는 iDRAC Dell 속성을 읽습니다.
func GetAttributes(ctx context.Context, c *redfish.Client) (*Attributes, error) {
	var attrs Attributes
	if err := c.Get(ctx, AttributesPath, &attrs); err != nil {
		return nil, err
	}
	return &attrs, nil
}

// String은 속성 값을 문자열로 반환합니다.
func (a *Attributes) String(key string) string {
	if a == nil || a.Attributes == nil {
		return ""
	}
	s, _ := a.Attributes[key].(string)
	return s
}

// SetAttributes는 Dell 속성을 변경합니다.
func SetAttributes(ctx context.Context, c *redfish.Client, values map[string]string) error {
	_, err := c.Patch(ctx, AttributesPath, map[string]any{"Attributes": values})
	if err != nil {
		return fmt.Errorf("iDRAC 속성 변경에 실패했습니다: %w", err)
	}
	return nil
}
