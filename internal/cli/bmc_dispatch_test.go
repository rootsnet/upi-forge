package cli

// BMC를 쓰는 네 명령(boot, live-boot, eject, inventory)이 실제로
// bmc.type으로 선택된 드라이버를 호출하는지 가짜 드라이버로 고정합니다.
// 어느 명령이든 선택된 드라이버를 지나치고 특정 장비 함수를 직접 호출하도록
// 바뀌면 여기서 실패합니다.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/logx"
	"upi-forge/internal/prompt"
	"upi-forge/internal/redfish"
)

// bmcCommandEnv는 BMC 명령이 작업 단계까지 도달하는 데 필요한 최소 구성
// (idracs.csv 포함 pathset과 boot용 노드 ISO)을 만듭니다.
func bmcCommandEnv(t *testing.T) (configPath string) {
	t.Helper()

	configDir := t.TempDir()
	workspace := t.TempDir()

	cfg := `
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
`
	if err := os.WriteFile(filepath.Join(configDir, "upi-forge.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	psDir := filepath.Join(configDir, "pathsets", "demo")
	if err := os.MkdirAll(psDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"pathset.yaml": `
name: "demo"
disk:
  osDisk: "/dev/disk/by-path/pci-0000:04:00.0"
network:
  gateway: "192.0.2.1"
  dns: ["192.0.2.7"]
  activeNIC: "enp1s0"
`,
		"nodes.csv":  "hostname,ip\nworker1,192.0.2.100\n",
		"nics.csv":   "nic\nenp1s0\n",
		"idracs.csv": "hostname,idrac_ip,idrac_id\nworker1,192.0.2.200,root\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(psDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// boot는 노드 설치 ISO가 있어야 작업 단계로 진행합니다.
	if err := os.WriteFile(filepath.Join(workspace, "worker1.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(configDir, "upi-forge.yaml")
}

// bmcCallCounts는 가짜 드라이버의 호출 횟수입니다.
type bmcCallCounts struct {
	boot, eject, nic, storage int
}

// installFakeBMC는 드라이버 선택과 세션 생성을 가짜로 바꿉니다.
// 세션을 실제로 만들지 않으므로 작업 함수 호출 여부만 남습니다.
func installFakeBMC(t *testing.T) *bmcCallCounts {
	t.Helper()
	calls := &bmcCallCounts{}

	origDriver, origSession := bmcDriverFor, withSession
	t.Cleanup(func() { bmcDriverFor, withSession = origDriver, origSession })

	bmcDriverFor = func(ps *config.Pathset) (*bmc.Driver, error) {
		return &bmc.Driver{
			Boot: func(context.Context, *redfish.Client, bmc.BootRequest) error {
				calls.boot++
				return nil
			},
			Eject: func(context.Context, *redfish.Client, time.Duration) error {
				calls.eject++
				return nil
			},
			NICInventory: func(context.Context, *redfish.Client) (*bmc.NICInventory, error) {
				calls.nic++
				return &bmc.NICInventory{}, nil
			},
			StorageInventory: func(context.Context, *redfish.Client) (*bmc.StorageInventory, error) {
				calls.storage++
				return &bmc.StorageInventory{}, nil
			},
			ConsoleName: "테스트 콘솔",
		}, nil
	}
	withSession = func(ctx context.Context, cfg *config.Config, target csvdata.IDRAC,
		password string, fn func(client *redfish.Client) error) error {
		return fn(nil)
	}
	t.Setenv(prompt.EnvCommonPassword, "test-password")
	return calls
}

func runBMCCommand(t *testing.T, configPath string, args ...string) error {
	t.Helper()
	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })

	err := Execute(context.Background(), append([]string{"--config", configPath}, args...))
	if err != nil {
		t.Logf("출력:\n%s", out.String())
	}
	return err
}

func TestBootDispatchesToSelectedBMC(t *testing.T) {
	configPath := bmcCommandEnv(t)
	calls := installFakeBMC(t)
	if err := runBMCCommand(t, configPath, "boot"); err != nil {
		t.Fatalf("boot 실패: %v", err)
	}
	if calls.boot != 1 {
		t.Errorf("선택된 드라이버의 Boot이 1회 호출되어야 합니다: %d", calls.boot)
	}
}

func TestLiveBootDispatchesToSelectedBMC(t *testing.T) {
	configPath := bmcCommandEnv(t)
	calls := installFakeBMC(t)
	if err := runBMCCommand(t, configPath, "live-boot"); err != nil {
		t.Fatalf("live-boot 실패: %v", err)
	}
	if calls.boot != 1 {
		t.Errorf("선택된 드라이버의 Boot이 1회 호출되어야 합니다: %d", calls.boot)
	}
}

func TestEjectDispatchesToSelectedBMC(t *testing.T) {
	configPath := bmcCommandEnv(t)
	calls := installFakeBMC(t)
	if err := runBMCCommand(t, configPath, "eject"); err != nil {
		t.Fatalf("eject 실패: %v", err)
	}
	if calls.eject != 1 {
		t.Errorf("선택된 드라이버의 Eject가 1회 호출되어야 합니다: %d", calls.eject)
	}
}

func TestInventoryDispatchesToSelectedBMC(t *testing.T) {
	configPath := bmcCommandEnv(t)
	calls := installFakeBMC(t)
	if err := runBMCCommand(t, configPath, "inventory"); err != nil {
		t.Fatalf("inventory 실패: %v", err)
	}
	if calls.nic != 1 || calls.storage != 1 {
		t.Errorf("선택된 드라이버의 인벤토리 수집이 각 1회 호출되어야 합니다: nic=%d storage=%d",
			calls.nic, calls.storage)
	}

	// 수집 결과는 NIC·스토리지 각각 YAML과 JSON 두 형식으로 저장되어야
	// 합니다. 저장 배선이 빠지면 여기서 실패합니다.
	outDir := filepath.Join(filepath.Dir(configPath), "pathsets", "demo", "inventory", "worker1")
	for _, name := range []string{
		"nic-inventory.yaml", "nic-inventory.json",
		"storage-inventory.yaml", "storage-inventory.json",
	} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("인벤토리 결과 파일이 저장되어야 합니다: %s: %v", name, err)
		}
	}
}
