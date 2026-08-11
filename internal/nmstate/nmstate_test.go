package nmstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"upi-forge/internal/config"
	"upi-forge/internal/nmstate"
)

// testdata의 골든 파일과 바이트 단위로 같은 YAML을 생성하는지 확인합니다.
var pathset3NICs = []string{
	"eno1np0", "eno2np1", "eno3np0",
	"eno4np1", "eno5np0", "eno6np1",
}

func baseNetwork() config.Network {
	return config.Network{
		Source:       config.NetworkGenerate,
		Gateway:      "192.0.2.1",
		PrefixLength: 24,
		DNS:          []string{"192.0.2.7", "192.0.2.6", "192.0.2.5"},
		ActiveNIC:    "eno5np0",
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("골든 파일을 읽지 못했습니다: %v", err)
	}
	return string(data)
}

func TestGenerateBondMatchesShellOutput(t *testing.T) {
	network := baseNetwork()
	network.Bonding = true
	network.Bond = config.Bond{
		Name:    "bond0",
		Mode:    "active-backup",
		Primary: "eno5np0",
		Standby: "eno6np1",
		MIIMon:  100,
	}

	got, err := nmstate.Generate(nmstate.Input{
		NICs:    pathset3NICs,
		IP:      "198.51.100.154",
		Network: network,
	})
	if err != nil {
		t.Fatalf("Generate 실패: %v", err)
	}
	if want := golden(t, "pathset3-bond.yaml"); string(got) != want {
		t.Errorf("bond 구성 출력이 골든 파일과 다릅니다.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestGenerateSingleNICMatchesShellOutput(t *testing.T) {
	got, err := nmstate.Generate(nmstate.Input{
		NICs:    pathset3NICs,
		IP:      "198.51.100.154",
		Network: baseNetwork(),
	})
	if err != nil {
		t.Fatalf("Generate 실패: %v", err)
	}
	if want := golden(t, "pathset3-single.yaml"); string(got) != want {
		t.Errorf("단일 NIC 출력이 골든 파일과 다릅니다.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestGenerateOmitsDNSResolverWhenEmpty(t *testing.T) {
	network := baseNetwork()
	network.DNS = nil

	got, err := nmstate.Generate(nmstate.Input{
		NICs:    []string{"enp1s0"},
		IP:      "192.0.2.31",
		Network: network,
	})
	if err != nil {
		t.Fatalf("Generate 실패: %v", err)
	}
	if contains(string(got), "dns-resolver") {
		t.Errorf("DNS가 비어 있으면 dns-resolver 블록이 없어야 합니다:\n%s", got)
	}
}

func TestGenerateRejectsMissingInput(t *testing.T) {
	if _, err := nmstate.Generate(nmstate.Input{NICs: []string{"enp1s0"}, Network: baseNetwork()}); err == nil {
		t.Error("IP가 없으면 오류를 반환해야 합니다")
	}
	if _, err := nmstate.Generate(nmstate.Input{IP: "192.0.2.31", Network: baseNetwork()}); err == nil {
		t.Error("NIC 목록이 없으면 오류를 반환해야 합니다")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
