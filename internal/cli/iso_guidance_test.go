package cli

// `upi-forge iso`의 완료 안내 분기(VM/베어메탈 × full/rootfs)를 고정하는
// 회귀 테스트입니다. 안내 문구는 운영자가 다음에 무엇을 해야 하는지 알려주는
// 실질적인 인터페이스이므로, 분기가 조용히 바뀌면 잘못된 절차로 이어집니다.
//
// coreos-installer 대역은 ignition 패키지의 butane 대역과 같은 방식으로
// 테스트 바이너리 자신을 사용합니다(TestMain 참고). 셸 스크립트 대역과 달리
// Windows에서도 그대로 동작합니다.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/logx"
)

const (
	isoTestNode   = "worker1.mycluster.example.com"
	testMCSSource = "https://api-int.mycluster.example.com:22623/config/worker"
)

// testIgnition은 출처 검증이 읽는 필드를 갖춘 노드 Ignition JSON을 만듭니다.
func testIgnition(hostname, mcsSource, caSource string) string {
	return `{"ignition":{"version":"3.5.0",` +
		`"config":{"merge":[{"source":"` + mcsSource + `"}]},` +
		`"security":{"tls":{"certificateAuthorities":[{"source":"` + caSource + `"}]}}},` +
		`"storage":{"files":[{"path":"/etc/hostname",` +
		`"contents":{"source":"data:text/plain;charset=utf-8,` + hostname + `"},"mode":420}]}}`
}

// isoGuidanceEnv는 하나의 안내 분기 시나리오에 필요한 파일 전체를 만듭니다.
//   - configDir: 설정, 템플릿, pathset(CSV 포함)
//   - workspace: full/minimal ISO, <host>.ign, <host>.yaml (출력 ISO는 없음)
func isoGuidanceEnv(t *testing.T, withIDRACs, useRootfs bool) (configPath string) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("테스트 바이너리 경로 확인 실패: %v", err)
	}
	configDir := t.TempDir()
	workspace := t.TempDir()

	// Windows 경로의 역슬래시가 이스케이프로 해석되지 않도록 작은따옴표를 씁니다.
	config := `
cluster:
  domain: "mycluster.example.com"
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
  coreosInstaller: '` + exe + `'
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
iso:
  useRootfs: ` + boolYAML(useRootfs) + `
disk:
  osDisk: "/dev/disk/by-path/pci-0000:04:00.0"
network:
  gateway: "192.0.2.1"
  dns: ["192.0.2.7"]
  activeNIC: "enp1s0"
`
	files := map[string]string{
		"pathset.yaml": pathset,
		"nodes.csv":    "hostname,ip\n" + isoTestNode + ",192.0.2.153\n",
		"nics.csv":     "nic\nenp1s0\n",
	}
	if withIDRACs {
		files["idracs.csv"] = "hostname,idrac_ip,idrac_id\n" + isoTestNode + ",198.51.100.110,root\n"
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(psDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 작업 디렉터리 산출물: 원본 ISO들과 노드별 ign/yaml.
	// .ign은 출처 검증(hostname·MCS 주소·CA)이 보는 필드를 갖춘 형태입니다.
	wsFiles := map[string]string{
		"coreos-x86_64.iso":         "fake-full-iso",
		"coreos-x86_64-minimal.iso": "fake-minimal-iso",
		isoTestNode + ".ign":        testIgnition(isoTestNode, testMCSSource, testCAURL(t)),
		isoTestNode + ".yaml":       "interfaces: []\n",
	}
	for name, content := range wsFiles {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(configDir, "upi-forge.yaml")
}

func boolYAML(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// runISOCapture는 iso 명령을 실행하고 안내 출력 전체를 돌려줍니다.
func runISOCapture(t *testing.T, configPath string) string {
	t.Helper()
	// exec된 테스트 바이너리가 coreos-installer 대역으로 동작하게 합니다.
	t.Setenv(envFakeInstaller, "1")

	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })

	// 안내 분기 테스트에는 MCS 서버가 없으므로 인증서 대조는 건너뜁니다.
	// (인증서 대조 자체는 iso_provenance_test.go가 검증합니다.)
	if err := Execute(context.Background(), []string{"--config", configPath, "iso", "--skip-ca-check"}); err != nil {
		t.Fatalf("iso 실행 실패: %v\n출력:\n%s", err, out.String())
	}
	return out.String()
}

func TestISOGuidanceVMFullMode(t *testing.T) {
	out := runISOCapture(t, isoGuidanceEnv(t, false, false))

	if !strings.Contains(out, "VM 대상으로 간주") {
		t.Errorf("idracs.csv가 없으면 VM 안내여야 합니다:\n%s", out)
	}
	if !strings.Contains(out, isoTestNode+".iso") || strings.Contains(out, "http://198.51.100.179:58080/iso/"+isoTestNode) {
		t.Errorf("VM 안내는 게시 URL이 아니라 로컬 ISO 경로를 보여야 합니다:\n%s", out)
	}
	if strings.Contains(out, "upi-forge boot") {
		t.Errorf("VM 안내에 boot 명령이 나오면 안 됩니다:\n%s", out)
	}
	if strings.Contains(out, "rootfs 분리 모드입니다") {
		t.Errorf("full 모드에는 rootfs 게시 안내가 없어야 합니다:\n%s", out)
	}
	if !strings.Contains(out, "가상 CD/DVD를 분리") {
		t.Errorf("설치 후 미디어 분리 안내가 있어야 합니다:\n%s", out)
	}
}

// VM 대상이라도 rootfs 분리 모드면 부팅한 노드가 웹 서버에서 rootfs.img를
// 내려받아야 하므로, 게시 안내가 반드시 나와야 합니다.
func TestISOGuidanceVMRootfsMode(t *testing.T) {
	out := runISOCapture(t, isoGuidanceEnv(t, false, true))

	if !strings.Contains(out, "VM 대상으로 간주") {
		t.Errorf("idracs.csv가 없으면 VM 안내여야 합니다:\n%s", out)
	}
	if !strings.Contains(out, "rootfs 분리 모드입니다") {
		t.Errorf("rootfs 모드면 VM 안내에도 rootfs 게시 안내가 있어야 합니다:\n%s", out)
	}
	if !strings.Contains(out, "http://198.51.100.179:58080/iso/coreos-x86_64-rootfs.img") {
		t.Errorf("노드가 내려받을 rootfs URL이 안내에 있어야 합니다:\n%s", out)
	}
}

func TestISOGuidanceIDRACFullMode(t *testing.T) {
	out := runISOCapture(t, isoGuidanceEnv(t, true, false))

	if !strings.Contains(out, "upi-forge boot") {
		t.Errorf("idracs.csv가 있으면 boot 안내여야 합니다:\n%s", out)
	}
	if !strings.Contains(out, "http://198.51.100.179:58080/iso/"+isoTestNode+".iso") {
		t.Errorf("게시 URL이 안내에 있어야 합니다:\n%s", out)
	}
	if strings.Contains(out, "VM 대상으로 간주") {
		t.Errorf("베어메탈 안내에 VM 문구가 나오면 안 됩니다:\n%s", out)
	}
	if strings.Contains(out, "rootfs 분리 모드입니다") {
		t.Errorf("full 모드에는 rootfs 게시 안내가 없어야 합니다:\n%s", out)
	}
}

func TestISOGuidanceIDRACRootfsMode(t *testing.T) {
	out := runISOCapture(t, isoGuidanceEnv(t, true, true))

	if !strings.Contains(out, "upi-forge boot") {
		t.Errorf("idracs.csv가 있으면 boot 안내여야 합니다:\n%s", out)
	}
	if !strings.Contains(out, "rootfs 분리 모드입니다") ||
		!strings.Contains(out, "http://198.51.100.179:58080/iso/coreos-x86_64-rootfs.img") {
		t.Errorf("rootfs 모드면 rootfs 게시 안내가 있어야 합니다:\n%s", out)
	}
}
