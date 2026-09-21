// Package idrac9는 Dell PowerEdge의 iDRAC9에서 사용하던 검증된 절차
// (현장에서 사용하던 Dell Redfish 절차)를 bmc 중립 계층의 드라이버로 옮긴
// 것입니다. iDRAC10 드라이버(internal/idrac)와 다른 점은 다음과 같습니다.
//
//   - 원타임 가상 미디어 부팅은 DellAttributes PATCH가 아니라 SCP
//     (Server Configuration Profile) Import — Manager의 OEM 액션
//     EID_674_Manager.ImportSystemConfiguration — 로 설정합니다.
//     iDRAC9에서 현장 검증된 방식입니다. Import는 비동기 작업이므로
//     응답 Location의 Task가 Completed가 될 때까지 기다린 뒤에만 전원을
//     켜고, 실패·시간 초과면 중단합니다. 요청에는 HostPowerState=Off를
//     명시해 iDRAC이 import 끝에 호스트를 먼저 켜지 않게 합니다.
//   - Virtual Media 컬렉션 위치가 펌웨어 세대에 따라 다릅니다(Dell 참조
//     스크립트 기준 6.00 미만은 Managers/iDRAC.Embedded.1/VirtualMedia,
//     이상은 Systems/System.Embedded.1/VirtualMedia). System이 광고한
//     참조 → Systems 표준 경로 → Managers 구형 경로 순으로 찾되, 404일
//     때만 다음 후보로 넘어갑니다.
//   - Virtual Media 액션(InsertMedia/EjectMedia)의 POST 대상은 Redfish
//     규약대로 장치가 광고한 Actions.target을 사용합니다.
//   - InsertMedia 본문은 현장 검증된 스크립트대로 Image만 보냅니다.
//     SCP 작업을 추적할 수 없고 DellAttributes 리소스도 없는 펌웨어에서는
//     확인 불가를 경고하고 진행합니다. 그 밖의 조회 실패는 작업을 중단합니다.
//   - 부트 순서 보호는 Settings 리소스가 없는 펌웨어(404)에서는 경고 후
//     건너뜁니다(문서 근거가 있는 대체 쓰기 경로를 확인하지 못함).
//   - iDRAC10 드라이버의 Virtual Media 안정성 속성 변경(Attached,
//     EncryptEnable)은 수행하지 않습니다. 검증된 스크립트에 없던 BMC 설정
//     변경이며 암호화 해제 부작용이 있기 때문입니다.
//
// System.Embedded.1 리소스 경로와 구조는 두 세대가 같으므로, 전원 제어와
// 시스템 조회, ISO 사전 확인, 인벤토리 수집은 internal/idrac의 공개
// 함수를 그대로 사용합니다. 비공개 도우미와 같은 동작이 필요한 부분은
// 검증된 iDRAC10 구현에 영향을 주지 않도록 이 패키지에서 별도로 구현하고,
// 주석에 공통 동작과 iDRAC9의 차이를 설명합니다.
package idrac9

import (
	"context"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/idrac"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

// Driver는 bmc 중립 계층에 연결하는 iDRAC9 드라이버입니다.
// cli는 pathset의 bmc.type이 idrac9일 때 이 드라이버를 사용합니다.
//
// 인벤토리는 iDRAC10 수집기를 그대로 사용합니다. Chassis 경로와 리소스
// 구조가 두 세대에서 같고 조회(GET)만 수행하기 때문입니다. 다만 실기
// 검증은 iDRAC10에서만 했으므로, iDRAC9 결과는 라이브 부팅 실측으로
// 확정하기 전까지 참고용입니다.
func Driver() *bmc.Driver {
	return &bmc.Driver{
		Boot:             Boot,
		Eject:            Eject,
		NICInventory:     idrac.CollectNICInventory,
		StorageInventory: idrac.CollectStorageInventory,
		ConsoleName:      "iDRAC Virtual Console",
		EjectHint:        "iDRAC 속성 ServerBoot.1.FirstBootDevice가 Normal인지도 확인하세요.",
	}
}

// iDRAC9에서 사용하는 Redfish 리소스 경로입니다.
// System 경로는 iDRAC10과 같으므로 idrac.SystemPath를 사용합니다.
const (
	// ManagerPath는 iDRAC 관리 컨트롤러 리소스입니다.
	ManagerPath = "/redfish/v1/Managers/iDRAC.Embedded.1"
	// ManagerVirtualMediaPath는 구형 펌웨어의 Virtual Media 컬렉션입니다.
	ManagerVirtualMediaPath = ManagerPath + "/VirtualMedia"
	// scpImportPath는 SCP Import 액션의 기본 경로입니다. Manager 리소스가
	// 광고하는 target을 우선 사용하고, 광고가 없을 때 이 값을 씁니다
	// (셸 스크립트가 검증한 고정 경로).
	scpImportPath = ManagerPath + "/Actions/Oem/EID_674_Manager.ImportSystemConfiguration"
)

// sleep은 컨텍스트가 취소되면 즉시 끝나는 대기입니다.
// iDRAC10 드라이버와 같은 방식으로 동작합니다.
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

// powerOffPollInterval은 전원 꺼짐 확인의 폴링 간격입니다.
// 테스트에서 짧게 바꿉니다.
var powerOffPollInterval = 2 * time.Second

// waitPowerOff는 PowerState가 Off가 될 때까지 limit 안에서 폴링합니다.
// 전원 상태를 확인하지 못해도 경고 후 진행하는 iDRAC10의 fail-open 정책을
// 그대로 따릅니다.
func waitPowerOff(ctx context.Context, c *redfish.Client, limit time.Duration) error {
	pollCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	deadline, _ := pollCtx.Deadline()

	state := "알 수 없음"
	for {
		sys, err := idrac.GetSystem(pollCtx, c)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if pollCtx.Err() != nil {
				logx.Warn("제한 시간 안에 전원 꺼짐이 확인되지 않았습니다(마지막 확인: %s). 계속 진행합니다.",
					state)
				return nil
			}
			logx.Warn("전원 상태를 확인하지 못했습니다. 계속 진행합니다: %v", err)
			return nil
		}
		state = sys.PowerState
		if state == "Off" {
			logx.Info("현재 전원 상태: %s", state)
			return nil
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			logx.Warn("제한 시간 안에 전원 꺼짐이 확인되지 않았습니다(현재: %s). 계속 진행합니다.", state)
			return nil
		}
		if err := sleep(pollCtx, min(powerOffPollInterval, remain)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logx.Warn("제한 시간 안에 전원 꺼짐이 확인되지 않았습니다(현재: %s). 계속 진행합니다.", state)
			return nil
		}
	}
}

// manager는 Manager 리소스에서 사용하는 필드입니다.
// SCP Import 액션의 실제 경로를 광고에서 읽습니다(장치가 광고한 target 우선).
type manager struct {
	Actions struct {
		Oem struct {
			ImportSystemConfiguration struct {
				Target string `json:"target"`
			} `json:"#OemManager.ImportSystemConfiguration"`
		} `json:"Oem"`
	} `json:"Actions"`
}

// scpImportTarget은 SCP Import 액션 경로를 결정합니다. Manager가 광고한
// target을 우선 사용하고, 조회 실패나 광고 부재 시에는 셸 스크립트가
// 검증한 고정 경로를 사용합니다(경고만 남기고 진행).
func scpImportTarget(ctx context.Context, c *redfish.Client) string {
	var m manager
	if err := c.Get(ctx, ManagerPath, &m); err != nil {
		logx.Warn("Manager 리소스를 조회하지 못해 기본 SCP Import 경로를 사용합니다: %v", err)
		return scpImportPath
	}
	if target := m.Actions.Oem.ImportSystemConfiguration.Target; target != "" {
		logx.Debug("SCP Import 경로(광고됨): %s", target)
		return target
	}
	logx.Debug("SCP Import 경로 광고가 없어 기본 경로를 사용합니다: %s", scpImportPath)
	return scpImportPath
}
