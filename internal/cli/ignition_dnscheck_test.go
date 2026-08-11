package cli

// `upi-forge ignition`이 산출물 생성 전에 DNS 등록 검증을 실제로 수행하는지,
// --skip-dns-check가 검증만 건너뛰는지 고정하는 배선(wiring) 테스트입니다.
// dnscheck 패키지의 검증 규칙 자체는 dnscheck_test.go가 고정합니다.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/logx"
)

// ignitionEnv는 ignition 명령이 DNS 검증 단계까지 도달하는 데 필요한
// 최소 파일을 만듭니다. DNS 서버는 127.0.0.1, 도메인은 .invalid TLD라서
// 검증은 반드시 실패합니다(RFC 2606: .invalid는 어떤 리졸버에서도
// 절대 해석되지 않음). oc는 존재하지 않는 경로라서 검증을 건너뛰면
// 다음 단계인 클러스터 사전 점검에서 다른 오류로 실패합니다.
func ignitionEnv(t *testing.T) (configPath string) {
	return ignitionEnvMode(t, false)
}

func ignitionEnvMode(t *testing.T, copyMode bool) (configPath string) {
	t.Helper()

	configDir := t.TempDir()
	workspace := t.TempDir()

	template := `{
  "ignition": {
    "version": "3.5.0",
    "config": {"merge": [{"source": "__MCS_SOURCE__"}]},
    "security": {"tls": {"certificateAuthorities": [{"source": "__MCS_CA_SOURCE__"}]}}
  },
  "storage": {"files": [{
    "overwrite": true,
    "path": "/etc/hostname",
    "user": {"name": "root"},
    "contents": {"source": "data:text/plain;charset=utf-8,__NODE_FQDN__"},
    "mode": 420
  }]}
}`
	if err := os.MkdirAll(filepath.Join(configDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "templates", "worker-pointer.ign.template"),
		[]byte(template), 0o600); err != nil {
		t.Fatal(err)
	}

	config := `
cluster:
  domain: "upi-forge-test.invalid"
registry:
  pullSecret: "pull-secret.json"
webServer:
  url: "http://198.51.100.179:58080"
  path: "/iso"
workspace:
  dir: '` + workspace + `'
pathsets:
  active: "demo"
tools:
  oc: '` + filepath.Join(configDir, "no-such-oc") + `'
`
	if err := os.WriteFile(filepath.Join(configDir, "upi-forge.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	psDir := filepath.Join(configDir, "pathsets", "demo")
	if err := os.MkdirAll(psDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathset := `
name: "demo"
disk:
  osDisk: "/dev/disk/by-path/pci-0000:04:00.0"
network:
  gateway: "192.0.2.1"
  dns: ["127.0.0.1"]
  activeNIC: "enp1s0"
`
	if copyMode {
		// copy 모드는 네트워크 값 검증을 건너뛰므로 dns 없이 구성될 수 있습니다.
		pathset = `
name: "demo"
disk:
  osDisk: "/dev/disk/by-path/pci-0000:04:00.0"
network:
  source: "copy"
`
	}
	files := map[string]string{
		"pathset.yaml": pathset,
		"nodes.csv":    "hostname,ip\nworker1,192.0.2.1\n",
		"nics.csv":     "nic\nenp1s0\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(psDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(configDir, "upi-forge.yaml")
}

func runIgnitionCapture(t *testing.T, configPath string, extra ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })

	args := append([]string{"--config", configPath, "ignition"}, extra...)
	err := Execute(context.Background(), args)
	return out.String(), err
}

// DNS 검증이 산출물 생성 전에 실행되어 실패를 막아야 합니다.
// 이 배선을 제거하면 오류가 사전 점검(oc) 단계의 것으로 바뀌어 실패합니다.
func TestIgnitionRunsDNSCheck(t *testing.T) {
	out, err := runIgnitionCapture(t, ignitionEnv(t))
	if err == nil {
		t.Fatalf("DNS 검증이 실패해야 합니다\n출력:\n%s", out)
	}
	if !strings.Contains(err.Error(), "DNS 검증에 실패했습니다") {
		t.Fatalf("DNS 검증 오류가 나와야 합니다: %v", err)
	}
	// 짧은 hostname에는 cluster.domain을 붙인 임시 FQDN을 써야 합니다.
	if !strings.Contains(err.Error(), "worker1.upi-forge-test.invalid") {
		t.Fatalf("임시 FQDN으로 검증해야 합니다: %v", err)
	}
}

// --skip-dns-check는 DNS 검증만 건너뛰고 나머지 단계는 그대로 진행합니다.
// (다음 단계인 클러스터 사전 점검이 없는 oc 경로로 실패하는 것으로 확인)
func TestIgnitionSkipDNSCheck(t *testing.T) {
	out, err := runIgnitionCapture(t, ignitionEnv(t), "--skip-dns-check")
	if err == nil {
		t.Fatalf("사전 점검(oc 없음)에서 실패해야 합니다\n출력:\n%s", out)
	}
	if strings.Contains(err.Error(), "DNS 검증에 실패했습니다") {
		t.Fatalf("--skip-dns-check면 DNS 검증 오류가 나오면 안 됩니다: %v", err)
	}
	if !strings.Contains(out, "DNS 등록 검증을 건너뜁니다") {
		t.Fatalf("건너뜀 경고가 출력되어야 합니다:\n%s", out)
	}
}

// FQDN의 도메인이 cluster.domain과 다르면 산출물 생성 전에 중단해야 합니다.
// 이 검사는 DNS 조회와 무관하므로 --skip-dns-check로도 건너뛸 수 없습니다.
func TestIgnitionRejectsForeignClusterFQDN(t *testing.T) {
	configPath := ignitionEnv(t)
	// cluster.domain은 upi-forge-test.invalid인데 다른 도메인의 FQDN을 넣습니다.
	nodesCSV := filepath.Join(filepath.Dir(configPath), "pathsets", "demo", "nodes.csv")
	if err := os.WriteFile(nodesCSV,
		[]byte("hostname,ip\nworker2.other.example.com,192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runIgnitionCapture(t, configPath, "--skip-dns-check")
	if err == nil {
		t.Fatalf("다른 클러스터 도메인의 FQDN은 중단되어야 합니다\n출력:\n%s", out)
	}
	if !strings.Contains(err.Error(), "클러스터 도메인과 다릅니다") {
		t.Fatalf("도메인 불일치 오류가 나와야 합니다: %v", err)
	}
	if !strings.Contains(err.Error(), "worker2.other.example.com") {
		t.Fatalf("문제의 hostname이 오류에 있어야 합니다: %v", err)
	}
	// 어떤 pathset의 어떤 파일을 읽었는지 표시해야 합니다.
	if !strings.Contains(err.Error(), "선택 pathset: demo") ||
		!strings.Contains(err.Error(), nodesCSV) {
		t.Fatalf("읽은 pathset과 파일 경로가 오류에 있어야 합니다: %v", err)
	}
}

// 클러스터 도메인 소속 FQDN은 도메인 검사를 통과하고 다음 단계(DNS 검증)로
// 진행해야 합니다.
func TestIgnitionAcceptsMatchingClusterFQDN(t *testing.T) {
	configPath := ignitionEnv(t)
	nodesCSV := filepath.Join(filepath.Dir(configPath), "pathsets", "demo", "nodes.csv")
	if err := os.WriteFile(nodesCSV,
		[]byte("hostname,ip\nworker1.upi-forge-test.invalid,192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runIgnitionCapture(t, configPath)
	if err == nil {
		t.Fatal("이 환경에서는 DNS 검증 단계에서 실패해야 합니다")
	}
	if strings.Contains(err.Error(), "클러스터 도메인과 다릅니다") {
		t.Fatalf("일치하는 FQDN이 도메인 검사에 걸리면 안 됩니다: %v", err)
	}
	if !strings.Contains(err.Error(), "DNS 검증에 실패했습니다") {
		t.Fatalf("도메인 검사를 통과해 DNS 검증 단계에 도달해야 합니다: %v", err)
	}
}

// 회귀 테스트: copy 모드 pathset은 network.dns가
// 없을 수 있고, 그 경우 ignition은 DNS 검증 오류로 실패하는 대신
// 경고 후 건너뛰고 다음 단계로 진행해야 합니다.
func TestIgnitionCopyModeWithoutDNSSkipsCheck(t *testing.T) {
	out, err := runIgnitionCapture(t, ignitionEnvMode(t, true))
	if err == nil {
		t.Fatalf("사전 점검(oc 없음)에서 실패해야 합니다\n출력:\n%s", out)
	}
	if strings.Contains(err.Error(), "DNS 검증") || strings.Contains(err.Error(), "network.dns") {
		t.Fatalf("dns 없는 copy pathset이 DNS 검증에서 막히면 안 됩니다: %v", err)
	}
	if !strings.Contains(out, "network.dns가 없어") {
		t.Fatalf("건너뜀 경고가 출력되어야 합니다:\n%s", out)
	}
}
