package yamlx_test

import (
	"testing"

	"upi-forge/internal/yamlx"
)

// 표준 YAML에서 흔한 "키와 같은 들여쓰기의 목록"이 처리되는지 확인합니다.
//
//	dns:
//	- 1.1.1.1
func TestSameIndentSequence(t *testing.T) {
	doc := "network:\n  dns:\n  - 192.0.2.7\n  - 192.0.2.6\n  gateway: 192.0.2.1\n"
	root, err := yamlx.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("동일 들여쓰기 목록 파싱 실패: %v", err)
	}
	var dns []string
	var gw string
	m := yamlx.NewMapper(root)
	network := m.Section("network")
	network.Strings("dns", &dns)
	network.String("gateway", &gw)
	if err := m.Finish(); err != nil {
		t.Fatalf("Finish 실패: %v", err)
	}
	if len(dns) != 2 || dns[0] != "192.0.2.7" {
		t.Errorf("dns: %v", dns)
	}
	if gw != "192.0.2.1" {
		t.Errorf("gateway: %q", gw)
	}
}
