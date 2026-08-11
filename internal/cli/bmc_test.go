package cli

// pathset의 bmc.type으로 BMC 드라이버를 선택하는 배선을 고정합니다.
// bmc.type 값 자체의 검증(기본값, 오타 거부)은 config 테스트가 고정합니다.

import (
	"strings"
	"testing"

	"upi-forge/internal/config"
)

// 지원 목록(config.SupportedBMCTypes)에 등록된 모든 종류는 빠짐없이
// 완전한 드라이버가 연결되어 있어야 합니다. 새 장비를 목록에만 등록하고
// resolveBMCDriver에 연결하지 않거나, 드라이버의 작업 일부를 비워 두면
// 이 테스트가 실패합니다.
func TestBMCDriversCoverSupportedTypes(t *testing.T) {
	for _, typ := range config.SupportedBMCTypes {
		ps := &config.Pathset{Name: "demo", BMC: config.BMC{Type: typ}}
		driver, err := resolveBMCDriver(ps)
		if err != nil {
			t.Fatalf("%s: 지원 목록의 종류는 드라이버가 있어야 합니다: %v", typ, err)
		}
		if driver.Boot == nil || driver.Eject == nil ||
			driver.NICInventory == nil || driver.StorageInventory == nil {
			t.Errorf("%s: 모든 작업이 연결되어야 합니다: %+v", typ, driver)
		}
		if driver.ConsoleName == "" {
			t.Errorf("%s: 완료 안내에 쓸 ConsoleName이 있어야 합니다", typ)
		}
	}
}

// 설정 검증과 드라이버 목록이 어긋난 경우(검증은 통과했지만 구현이 없는
// 종류)의 오류에는 문제의 종류, pathset 이름, 지원 목록이 있어야 합니다.
func TestBMCDriverForUnknownType(t *testing.T) {
	ps := &config.Pathset{Name: "demo", BMC: config.BMC{Type: "ilo5"}}
	_, err := resolveBMCDriver(ps)
	if err == nil {
		t.Fatal("구현이 없는 종류는 오류여야 합니다")
	}
	for _, want := range []string{"ilo5", "demo", config.BMCTypeIDRAC10} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("오류에 %q가 있어야 합니다: %v", want, err)
		}
	}
}
