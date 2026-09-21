// Package bmc는 관리 컨트롤러(BMC) 작업의 장비 중립 계층입니다.
//
// 부팅 요청, 단계별 대기 시간, 인벤토리 문서처럼 장비 종류와 무관한
// 타입과, 장비별 구현이 채워 넣는 Driver 함수 묶음을 정의합니다.
// pathset의 bmc.type으로 드라이버를 선택하며, 현재 구현은 idrac(iDRAC10)과
// idrac9(iDRAC9)입니다. 다른 장비를 추가할 때는 그 장비 패키지가 이 계층의
// 타입으로 결과를 만들어 Driver를 채우고, config.SupportedBMCTypes와 cli의
// 드라이버 선택에 종류를 등록합니다.
//
// Redfish 클라이언트는 프로토콜 계층(세션, 출처 검증, 응답 상한)만
// 담당하므로 장비 종류와 무관하게 공유합니다. 리소스 경로와 부팅 절차가
// 장비마다 달라지는 부분이 드라이버의 몫입니다.
package bmc

import (
	"context"
	"time"

	"upi-forge/internal/redfish"
)

// BootRequest는 원타임 가상 미디어 부팅 요청입니다.
type BootRequest struct {
	// ISOURL은 BMC가 마운트할 ISO의 HTTP/HTTPS 주소입니다.
	ISOURL string
	// Waits는 각 단계 사이의 대기 시간입니다.
	Waits Waits
	// SkipISOCheck를 true로 하면 ISO URL 사전 확인을 건너뜁니다.
	SkipISOCheck bool
	// HTTPTimeout은 ISO URL 확인에 사용할 제한 시간입니다.
	HTTPTimeout time.Duration
}

// Waits는 BMC 작업 단계 사이의 대기 시간입니다.
// 기본값은 설정(redfish.*WaitSeconds)이 정합니다.
type Waits struct {
	PowerOff  time.Duration // 전원 OFF 후 대기
	Media     time.Duration // 미디어 삽입/제거 후 대기
	Attribute time.Duration // 설정(속성) 변경 반영 대기
	PowerOn   time.Duration // 전원 ON 후 대기
}

// Driver는 장비 종류 하나의 BMC 작업 구현입니다.
// 함수 필드는 모두 필수이며, cli 테스트가 등록된 드라이버의 완전성을
// 고정합니다.
type Driver struct {
	// Boot는 지정한 ISO로 서버를 원타임 가상 미디어 부팅합니다.
	Boot func(ctx context.Context, c *redfish.Client, req BootRequest) error
	// Eject는 연결된 가상 미디어를 모두 제거합니다.
	Eject func(ctx context.Context, c *redfish.Client, wait time.Duration) error
	// NICInventory는 NIC 인벤토리를 수집합니다. 조회만 수행해야 합니다.
	NICInventory func(ctx context.Context, c *redfish.Client) (*NICInventory, error)
	// StorageInventory는 스토리지 인벤토리를 수집합니다. 조회만 수행해야 합니다.
	StorageInventory func(ctx context.Context, c *redfish.Client) (*StorageInventory, error)

	// ConsoleName은 완료 안내에 쓰는 장비 콘솔 이름입니다.
	// 예) iDRAC Virtual Console
	ConsoleName string
	// EjectHint는 eject 완료 후 추가로 확인을 권할 장비별 안내입니다.
	// 비우면 안내를 생략합니다.
	EjectHint string
}
