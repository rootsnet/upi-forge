package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/config"
)

// writeTree는 실제 배포와 같은 모양의 설정 디렉터리를 만듭니다.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("디렉터리 생성 실패: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("파일 생성 실패: %v", err)
		}
	}
	return root
}

const minimalConfig = `
cluster:
  domain: "mycluster.example.com"
registry:
  pullSecret: "pull-secret.json"
webServer:
  url: "http://198.51.100.179:58080/"
  path: "iso/"
pathsets:
  active: "pathset-3"
`

const pathset3 = `
name: "pathset-3"
disk:
  osDisk: "/dev/disk/by-path/pci-0000:3c:00.0-nvme-1"
butane: "pathset-3.bu"
network:
  gateway: "192.0.2.1"
  prefixLength: 24
  dns:
    - "192.0.2.7"
  bonding: true
  activeNIC: "eno1np0"
  bond:
    name: "bond0"
    primary: "eno1np0"
    standby: "eno2np1"
`

func TestLoadAppliesDefaults(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml":                  minimalConfig,
		"pathsets/pathset-3/pathset.yaml": pathset3,
	})

	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}

	if got, want := cfg.Cluster.APIURL, "https://api.mycluster.example.com:6443"; got != want {
		t.Errorf("apiURL 기본값: got=%q want=%q", got, want)
	}
	if got, want := cfg.MCSSource(), "https://api-int.mycluster.example.com:22623/config/worker"; got != want {
		t.Errorf("MCSSource: got=%q want=%q", got, want)
	}
	if got, want := cfg.RootfsURL(), "http://198.51.100.179:58080/iso/coreos-x86_64-rootfs.img"; got != want {
		t.Errorf("RootfsURL: got=%q want=%q", got, want)
	}
	if got, want := cfg.PublishURL("worker1.iso"), "http://198.51.100.179:58080/iso/worker1.iso"; got != want {
		t.Errorf("PublishURL: got=%q want=%q", got, want)
	}
	if got := cfg.BaseRegistry(); got != "quay.io" {
		t.Errorf("BaseRegistry: %q", got)
	}
	// 상대 경로는 설정 파일 디렉터리 기준으로 해석해야 합니다.
	if got, want := cfg.PullSecretPath(), filepath.Join(root, "pull-secret.json"); got != want {
		t.Errorf("PullSecretPath: got=%q want=%q", got, want)
	}
}

func TestBaseRegistryUsesFirstPathElement(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml": minimalConfig + "\nregistry:\n  registry: \"mirror.example.com/ocp4\"\n",
	})
	// registry 키가 두 번 나오므로 파서가 중복으로 거부해야 합니다.
	if _, err := config.Load(filepath.Join(root, "upi-forge.yaml")); err == nil {
		t.Fatal("중복 키는 오류여야 합니다")
	}

	root = writeTree(t, map[string]string{
		"upi-forge.yaml": `
cluster:
  domain: "mycluster.example.com"
registry:
  pullSecret: "pull-secret.json"
  registry: "mirror.example.com/ocp4"
webServer:
  url: "http://web:8080"
  path: "/iso"
pathsets:
  active: "pathset-3"
`,
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	if got := cfg.BaseRegistry(); got != "mirror.example.com" {
		t.Errorf("BaseRegistry: %q", got)
	}
}

func TestLoadRejectsMissingRequiredFields(t *testing.T) {
	cases := map[string]string{
		"도메인 없음":         "registry:\n  pullSecret: x\nwebServer:\n  url: http://a\npathsets:\n  active: p\n",
		"pull secret 없음": "cluster:\n  domain: a.b\nwebServer:\n  url: http://a\npathsets:\n  active: p\n",
		"웹 서버 없음":        "cluster:\n  domain: a.b\nregistry:\n  pullSecret: x\npathsets:\n  active: p\n",
		"pathset 없음":     "cluster:\n  domain: a.b\nregistry:\n  pullSecret: x\nwebServer:\n  url: http://a\n",
		"알 수 없는 키":       minimalConfig + "unknownKey: 1\n",
		// apiURL이 cluster.domain 소속이 아니면 사전 점검과 MCS(도메인 파생)가
		// 서로 다른 클러스터를 보게 되므로 거부합니다.
		"apiURL 다른 도메인": strings.Replace(minimalConfig, `domain: "mycluster.example.com"`,
			"domain: \"mycluster.example.com\"\n  apiURL: \"https://api.other.example.com:6443\"", 1),
		"apiURL IP 주소": strings.Replace(minimalConfig, `domain: "mycluster.example.com"`,
			"domain: \"mycluster.example.com\"\n  apiURL: \"https://192.0.2.10:6443\"", 1),
		// 음수 시간은 즉시 만료된 제한 시간/대기가 되므로 거부합니다.
		"음수 redfish 대기": minimalConfig + "redfish:\n  powerOffWaitSeconds: -1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"upi-forge.yaml": content})
			if _, err := config.Load(filepath.Join(root, "upi-forge.yaml")); err == nil {
				t.Error("오류를 반환해야 합니다")
			}
		})
	}
}

// cluster.domain 소속의 apiURL은 포트나 호스트가 관례와 달라도 허용됩니다.
func TestLoadAcceptsAPIURLWithinClusterDomain(t *testing.T) {
	content := strings.Replace(minimalConfig, `domain: "mycluster.example.com"`,
		"domain: \"mycluster.example.com\"\n  apiURL: \"https://api-custom.mycluster.example.com:7443\"", 1)
	root := writeTree(t, map[string]string{"upi-forge.yaml": content})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("같은 도메인의 apiURL은 통과해야 합니다: %v", err)
	}
	if cfg.Cluster.APIURL != "https://api-custom.mycluster.example.com:7443" {
		t.Errorf("apiURL: %q", cfg.Cluster.APIURL)
	}
}

// tools.coreosInstaller는 "실행" 경로에만 영향을 주어야 합니다. prepare의
// "추출" 대상(WorkspaceInstaller)까지 바뀌면 release 이미지에서 추출한
// 바이너리가 시스템 도구를 덮어쓰게 됩니다.
func TestPathsExternalInstallerKeepsWorkspaceExtractTarget(t *testing.T) {
	// 외부 도구 경로는 실행 플랫폼 기준의 절대 경로로 만듭니다.
	// "/usr/bin/..." 같은 리터럴은 Windows에서 절대 경로가 아니라서
	// 설정 디렉터리 기준으로 해석되어 버립니다.
	external := filepath.Join(t.TempDir(), "coreos-installer")
	content := strings.Replace(minimalConfig, "pathsets:",
		"tools:\n  coreosInstaller: '"+external+"'\npathsets:", 1)
	root := writeTree(t, map[string]string{"upi-forge.yaml": content})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	paths, err := cfg.Paths()
	if err != nil {
		t.Fatalf("Paths 실패: %v", err)
	}
	if paths.Installer != external {
		t.Errorf("실행 경로는 외부 도구여야 합니다: got %q want %q", paths.Installer, external)
	}
	if want := filepath.Join(paths.Dir, "coreos-installer"); paths.WorkspaceInstaller != want {
		t.Errorf("추출 대상은 항상 작업 디렉터리여야 합니다: got %q want %q",
			paths.WorkspaceInstaller, want)
	}
}

func TestLoadPathset(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml":                  minimalConfig,
		"pathsets/pathset-3/pathset.yaml": pathset3,
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	ps, err := cfg.LoadPathset()
	if err != nil {
		t.Fatalf("LoadPathset 실패: %v", err)
	}

	if ps.Name != "pathset-3" {
		t.Errorf("name: %q", ps.Name)
	}
	if ps.Network.Source != config.NetworkGenerate {
		t.Errorf("network.source 기본값이 generate여야 합니다: %q", ps.Network.Source)
	}
	if ps.Network.Bond.Mode != "active-backup" || ps.Network.Bond.MIIMon != 100 {
		t.Errorf("bond 기본값: %+v", ps.Network.Bond)
	}
	if ps.RouteInterface() != "bond0" {
		t.Errorf("bond를 쓰면 route 인터페이스는 bond여야 합니다: %q", ps.RouteInterface())
	}
	if got, want := ps.NodesCSV(), filepath.Join(root, "pathsets/pathset-3/nodes.csv"); got != want {
		t.Errorf("NodesCSV: got=%q want=%q", got, want)
	}
	if got, want := ps.ButanePath(), filepath.Join(root, "pathsets/pathset-3/pathset-3.bu"); got != want {
		t.Errorf("ButanePath: got=%q want=%q", got, want)
	}
	if ps.BMC.Type != config.BMCTypeIDRAC10 {
		t.Errorf("bmc.type 기본값이 idrac10이어야 합니다: %q", ps.BMC.Type)
	}
}

func TestLoadPathsetValidation(t *testing.T) {
	cases := map[string]string{
		"osDisk 없음":    "name: p\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  activeNIC: eth0\n",
		"잘못된 source":   "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  source: merge\n",
		"DNS 없음":       "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  activeNIC: eth0\n",
		"bond 이름 충돌":   "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  bonding: true\n  bond:\n    name: eth0\n    primary: eth0\n    standby: eth1\n",
		"activeNIC 없음": "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n",
		// copy 모드는 네트워크 값 검증을 건너뛰지만, DNS 등록 검증이 쓰는
		// network.dns의 형식만은 확인합니다.
		"copy 모드 잘못된 DNS": "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  source: copy\n  dns: [not-an-ip]\n",
		// bmc.type 오타는 BMC 명령 실행 시점이 아니라 설정 검증 시점에 잡습니다.
		"지원하지 않는 bmc.type": "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  activeNIC: eth0\nbmc:\n  type: ilo5\n",
		// NMState 생성기는 active-backup 전제로 YAML을 만듭니다. 오타나
		// 다른 모드, 잘못된 miimon은 노드 부팅 후 NMState 적용 단계가
		// 아니라 설정 검증 시점에 잡습니다.
		"bond mode 오타":      "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  bonding: true\n  bond:\n    name: bond0\n    mode: active-bakcup\n    primary: eth0\n    standby: eth1\n",
		"지원하지 않는 bond mode": "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  bonding: true\n  bond:\n    name: bond0\n    mode: 802.3ad\n    primary: eth0\n    standby: eth1\n",
		"음수 miimon":         "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  bonding: true\n  bond:\n    name: bond0\n    primary: eth0\n    standby: eth1\n    miimon: -100\n",
		// 병렬 개수 오타(음수)는 BMC 명령 실행 시점이 아니라 설정 검증
		// 시점에 잡습니다.
		"음수 bmc.parallel": "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  activeNIC: eth0\nbmc:\n  parallel: -2\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeTree(t, map[string]string{
				"upi-forge.yaml":          strings.Replace(minimalConfig, "pathset-3", "p", 1),
				"pathsets/p/pathset.yaml": content,
			})
			cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
			if err != nil {
				t.Fatalf("Load 실패: %v", err)
			}
			if _, err := cfg.LoadPathset(); err == nil {
				t.Error("오류를 반환해야 합니다")
			}
		})
	}
}

// copy 모드는 network.dns 없이도 설정 검증을 통과해야 합니다.
// 이때 ignition은 DNS 등록 검증을 건너뛴다고 안내합니다(cli 테스트에서 고정).
func TestLoadPathsetCopyModeWithoutDNSPasses(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml":          strings.Replace(minimalConfig, "pathset-3", "p", 1),
		"pathsets/p/pathset.yaml": "name: p\ndisk:\n  osDisk: /dev/x\nnetwork:\n  source: copy\n",
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	ps, err := cfg.LoadPathset()
	if err != nil {
		t.Fatalf("copy 모드는 dns 없이 통과해야 합니다: %v", err)
	}
	if len(ps.Network.DNS) != 0 {
		t.Errorf("DNS 목록이 비어 있어야 합니다: %v", ps.Network.DNS)
	}
}

// bmc.type을 명시해도 통과해야 하고, 지원 목록의 값과 같아야 합니다.
func TestLoadPathsetExplicitBMCType(t *testing.T) {
	// 지원 목록의 모든 종류는 명시 시 그대로 통과해야 합니다.
	for _, typ := range []string{config.BMCTypeIDRAC10, config.BMCTypeIDRAC9} {
		root := writeTree(t, map[string]string{
			"upi-forge.yaml": strings.Replace(minimalConfig, "pathset-3", "p", 1),
			"pathsets/p/pathset.yaml": "name: p\ndisk:\n  osDisk: /dev/x\n" +
				"network:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  activeNIC: eth0\n" +
				"bmc:\n  type: \"" + typ + "\"\n",
		})
		cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
		if err != nil {
			t.Fatalf("Load 실패: %v", err)
		}
		ps, err := cfg.LoadPathset()
		if err != nil {
			t.Fatalf("명시한 %s은(는) 통과해야 합니다: %v", typ, err)
		}
		if ps.BMC.Type != typ {
			t.Errorf("bmc.type: %q", ps.BMC.Type)
		}
	}
	// 기본값 확인은 마지막 pathset(idrac9)으로 이어집니다.
	root := writeTree(t, map[string]string{
		"upi-forge.yaml": strings.Replace(minimalConfig, "pathset-3", "p", 1),
		"pathsets/p/pathset.yaml": "name: p\ndisk:\n  osDisk: /dev/x\n" +
			"network:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  activeNIC: eth0\n" +
			"bmc:\n  type: \"idrac10\"\n",
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	ps, err := cfg.LoadPathset()
	if err != nil {
		t.Fatalf("명시한 idrac10은 통과해야 합니다: %v", err)
	}
	if ps.BMC.Type != config.BMCTypeIDRAC10 {
		t.Errorf("bmc.type: %q", ps.BMC.Type)
	}
	// 실행 방식 옵션의 기본값: 순차(0), 노드별 비밀번호 입력(false).
	if ps.BMC.Parallel != 0 || ps.BMC.SamePassword {
		t.Errorf("bmc 실행 옵션 기본값이 순차/노드별 입력이어야 합니다: parallel=%d samePassword=%v",
			ps.BMC.Parallel, ps.BMC.SamePassword)
	}
}

// bmc.samePassword와 bmc.parallel이 그대로 읽혀야 합니다.
func TestLoadPathsetBMCRunOptions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml": strings.Replace(minimalConfig, "pathset-3", "p", 1),
		"pathsets/p/pathset.yaml": "name: p\ndisk:\n  osDisk: /dev/x\n" +
			"network:\n  gateway: 192.0.2.1\n  dns: [1.1.1.1]\n  activeNIC: eth0\n" +
			"bmc:\n  samePassword: true\n  parallel: 4\n",
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	ps, err := cfg.LoadPathset()
	if err != nil {
		t.Fatalf("LoadPathset 실패: %v", err)
	}
	if !ps.BMC.SamePassword || ps.BMC.Parallel != 4 {
		t.Errorf("bmc 실행 옵션이 반영되어야 합니다: parallel=%d samePassword=%v",
			ps.BMC.Parallel, ps.BMC.SamePassword)
	}
}

func TestLoadPathsetRejectsNameMismatch(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml":                  minimalConfig,
		"pathsets/pathset-3/pathset.yaml": strings.Replace(pathset3, `"pathset-3"`, `"pathset-9"`, 1),
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	if _, err := cfg.LoadPathset(); err == nil {
		t.Error("디렉터리 이름과 다르면 오류여야 합니다")
	}
}

func TestPathsetTraversalIsRejected(t *testing.T) {
	root := writeTree(t, map[string]string{"upi-forge.yaml": minimalConfig})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	if _, err := cfg.LoadPathsetNamed("../etc"); err == nil {
		t.Error("경로 이동은 오류여야 합니다")
	}
}

func TestNodePaths(t *testing.T) {
	root := writeTree(t, map[string]string{"upi-forge.yaml": minimalConfig})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	paths, err := cfg.Paths()
	if err != nil {
		t.Fatalf("Paths 실패: %v", err)
	}
	host := "worker1.mycluster.example.com"
	if filepath.Base(paths.NodeIgnition(host)) != host+".ign" {
		t.Errorf("NodeIgnition: %s", paths.NodeIgnition(host))
	}
	if filepath.Base(paths.NodeNMState(host)) != host+".yaml" {
		t.Errorf("NodeNMState: %s", paths.NodeNMState(host))
	}
	if filepath.Base(paths.NodeISO(host)) != host+".iso" {
		t.Errorf("NodeISO: %s", paths.NodeISO(host))
	}
}

// TestWorkspaceEntriesMustStayInsideWorkspace는 설정 실수로 작업 디렉터리
// 전체나 상위가 삭제되는 사고를 막는 검사를 확인합니다.
//
// prepare 단계는 workspace.tmpDir를 os.RemoveAll로 디렉터리 전체를 삭제합니다.
// tmpDir가 "." 이면 작업 디렉터리 전체가, ".." 이면 그 상위가 사라집니다.
func TestTmpDirMustStayInsideWorkspace(t *testing.T) {
	dangerous := map[string]string{
		"현재 디렉터리": `workspace:
  tmpDir: "."
`,
		"상위 디렉터리": `workspace:
  tmpDir: ".."
`,
		"상위로 올라가기": `workspace:
  tmpDir: "../shared-tmp"
`,
		"절대 경로": `workspace:
  tmpDir: "/var/tmp/upi"
`,
		"우회 경로": `workspace:
  tmpDir: "tmp/../.."
`,
	}
	for name, extra := range dangerous {
		t.Run(name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"upi-forge.yaml": minimalConfig + extra})
			if _, err := config.Load(filepath.Join(root, "upi-forge.yaml")); err == nil {
				t.Errorf("작업 디렉터리 밖을 가리키면 오류여야 합니다:\n%s", extra)
			}
		})
	}
}

func TestTmpDirAllowsNestedSubdirectory(t *testing.T) {
	root := writeTree(t, map[string]string{
		"upi-forge.yaml": minimalConfig + `workspace:
  tmpDir: "work/tmp"
`,
	})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("작업 디렉터리 하위 경로는 허용해야 합니다: %v", err)
	}
	paths, err := cfg.Paths()
	if err != nil {
		t.Fatalf("Paths 실패: %v", err)
	}
	if filepath.Base(paths.TmpDir) != "tmp" {
		t.Errorf("TmpDir: %s", paths.TmpDir)
	}
	if !strings.HasPrefix(paths.TmpDir, paths.Dir+string(filepath.Separator)) {
		t.Errorf("TmpDir가 작업 디렉터리 하위여야 합니다: %s (dir=%s)", paths.TmpDir, paths.Dir)
	}
}

// TestDefaultTmpDirStillWorks는 방어 코드가 정상 기본값까지 막지 않는지 확인합니다.
func TestDefaultTmpDirStillWorks(t *testing.T) {
	root := writeTree(t, map[string]string{"upi-forge.yaml": minimalConfig})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	paths, err := cfg.Paths()
	if err != nil {
		t.Fatalf("Paths 실패: %v", err)
	}
	if filepath.Base(paths.TmpDir) != "tmp" {
		t.Errorf("기본 tmpDir는 tmp여야 합니다: %s", paths.TmpDir)
	}
}

// TestWebServerURLRequiresHTTPScheme은 오류 메시지의 안내("http:// 또는 https://")와
// 실제 검증이 일치하는지 확인합니다. iDRAC Virtual Media는 HTTP(S) URL만
// 받으므로 ftp:// 같은 다른 scheme은 설정 단계에서 걸러야 합니다.
func TestWebServerURLRequiresHTTPScheme(t *testing.T) {
	cases := map[string]bool{ // url -> 허용 여부
		"http://198.51.100.179:58080": true,
		"https://web.example.com":     true,
		"ftp://198.51.100.179":        false,
		"nfs://198.51.100.179/iso":    false,
		"file:///var/www/html/iso":    false,
		"198.51.100.179:58080":        false,
	}
	for rawURL, want := range cases {
		t.Run(rawURL, func(t *testing.T) {
			content := strings.Replace(minimalConfig,
				`url: "http://198.51.100.179:58080/"`, `url: "`+rawURL+`"`, 1)
			root := writeTree(t, map[string]string{"upi-forge.yaml": content})
			_, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
			if want && err != nil {
				t.Errorf("허용해야 합니다: %v", err)
			}
			if !want && err == nil {
				t.Errorf("scheme %q는 거부해야 합니다", rawURL)
			}
		})
	}
}
