package idrac

import (
	"testing"

	"upi-forge/internal/bmc"
)

func strPtr(v string) *string { return &v }

// 링크 접두사 매칭은 경로 경계를 구분해야 합니다. ".../Storage/CPU.1"이
// ".../Storage/CPU.10"의 링크와 짝지어지면 dest-device 후보의 by-path
// 예상값이 다른 디스크의 PCI 주소로 표시됩니다.
func TestMatchPCIeFunctionRespectsPathBoundary(t *testing.T) {
	base := "/redfish/v1/Systems/System.Embedded.1/Storage/"
	functions := []bmc.PCIeFunction{
		{Name: "cpu10", StorageControllerLinks: []string{base + "CPU.10#/StorageControllers/0"}},
		{Name: "cpu1", StorageControllerLinks: []string{base + "CPU.1#/StorageControllers/0"}},
	}

	got := matchPCIeFunction(functions, "CPU.1", base+"CPU.1")
	if got == nil || got.Name != "cpu1" {
		t.Errorf("CPU.1은 자기 링크와 짝지어져야 합니다: %+v", got)
	}
	got = matchPCIeFunction(functions, "CPU.10", base+"CPU.10")
	if got == nil || got.Name != "cpu10" {
		t.Errorf("CPU.10은 자기 링크와 짝지어져야 합니다: %+v", got)
	}

	// 정확히 일치하는 링크와 "/" 경계 링크도 허용해야 합니다.
	exact := []bmc.PCIeFunction{{Name: "exact", StorageControllerLinks: []string{base + "CPU.2"}}}
	if got := matchPCIeFunction(exact, "CPU.2", base+"CPU.2"); got == nil {
		t.Error("완전 일치 링크가 매칭되어야 합니다")
	}

	// 링크가 없어도 OEM ControllerID 일치로 매칭되어야 합니다.
	byID := []bmc.PCIeFunction{
		{Name: "other"},
		{Name: "right", ControllerID: strPtr("BOSS.Slot.3-1")},
	}
	if got := matchPCIeFunction(byID, "BOSS.Slot.3-1", base+"BOSS.Slot.3-1"); got == nil || got.Name != "right" {
		t.Errorf("ControllerID 일치로 매칭되어야 합니다: %+v", got)
	}
}

// PCI 주소를 해석하지 못하면 "pci-"까지만 붙은 절반의 경로 대신
// 빈 값을 유지해야 합니다.
func TestByPathPrefix(t *testing.T) {
	if got := byPathPrefix("0000:3c:00.0"); got != "/dev/disk/by-path/pci-0000:3c:00.0" {
		t.Errorf("byPathPrefix: %q", got)
	}
	if got := byPathPrefix(""); got != "" {
		t.Errorf("주소가 없으면 빈 값이어야 합니다: %q", got)
	}
}
