package cli

// 도메인 검사가 ignition뿐 아니라 iso/boot에도 배선되어 있는지, 그리고
// 배포용 예시 configs가 자기모순이 아닌지 고정합니다.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/logx"
)

const foreignNode = "worker9.other.example.com"

// iso도 도메인 검사를 수행해 다른 클러스터 이름의 기존 Ignition을 거부해야
// 합니다.
func TestISORejectsForeignClusterFQDN(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	psDir := filepath.Join(filepath.Dir(configPath), "pathsets", "demo")
	if err := os.WriteFile(filepath.Join(psDir, "nodes.csv"),
		[]byte("hostname,ip\n"+foreignNode+",192.0.2.153\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })
	err := Execute(context.Background(), []string{"--config", configPath, "iso"})
	if err == nil || !strings.Contains(err.Error(), "클러스터 도메인과 다릅니다") {
		t.Fatalf("iso도 다른 클러스터 FQDN을 거부해야 합니다: %v", err)
	}
}

// boot도 도메인 검사를 수행해 다른 클러스터 이름의 설치 ISO를 거부해야 합니다.
func TestBootRejectsForeignClusterFQDN(t *testing.T) {
	configPath := isoGuidanceEnv(t, true, false)
	psDir := filepath.Join(filepath.Dir(configPath), "pathsets", "demo")
	files := map[string]string{
		"nodes.csv":  "hostname,ip\n" + foreignNode + ",192.0.2.153\n",
		"idracs.csv": "hostname,idrac_ip,idrac_id\n" + foreignNode + ",198.51.100.110,root\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(psDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })
	err := Execute(context.Background(), []string{"--config", configPath, "boot"})
	if err == nil || !strings.Contains(err.Error(), "클러스터 도메인과 다릅니다") {
		t.Fatalf("boot도 다른 클러스터 FQDN을 거부해야 합니다: %v", err)
	}
}

// 배포용 예시 configs는 cluster.domain, nodes.csv, idracs.csv가 서로 일치해야
// 합니다. 비활성 pathset의 불일치도 놓치지 않도록 모든 pathset을 검사합니다.
func TestShippedExampleConfigsConsistent(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "upi-forge.yaml")
	if !fsutil.IsRegularFile(configPath) {
		t.Fatalf("배포용 예시 설정이 없습니다: %s", configPath)
	}
	entries, err := os.ReadDir(filepath.Join("..", "..", "configs", "pathsets"))
	if err != nil {
		t.Fatalf("예시 pathsets 디렉터리를 읽지 못했습니다: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		checked++
		t.Run(entry.Name(), func(t *testing.T) {
			// pathset마다 설정을 새로 로드하고 active만 바꿔 검사합니다.
			cfg, err := config.Load(configPath)
			if err != nil {
				t.Fatalf("예시 설정 로드 실패: %v", err)
			}
			cfg.Pathsets.Active = entry.Name()
			ps, err := cfg.LoadPathset()
			if err != nil {
				t.Fatalf("예시 pathset 로드 실패: %v", err)
			}
			nodes, err := csvdata.LoadNodes(ps.NodesCSV())
			if err != nil {
				t.Fatalf("예시 nodes.csv 로드 실패: %v", err)
			}
			if err := checkNodeDomains(ps.Name, nodes, cfg.Cluster.Domain); err != nil {
				t.Errorf("예시 nodes.csv의 hostname이 cluster.domain과 어긋납니다: %v", err)
			}
			if fsutil.IsRegularFile(ps.IDRACsCSV()) {
				idracs, err := csvdata.LoadIDRACs(ps.IDRACsCSV())
				if err != nil {
					t.Fatalf("예시 idracs.csv 로드 실패: %v", err)
				}
				if err := idracs.MatchNodes(nodes); err != nil {
					t.Errorf("예시 nodes.csv와 idracs.csv가 어긋납니다: %v", err)
				}
			}
			// generate 모드 pathset은 선택한 NIC이 nics.csv 목록에
			// 있어야 합니다. 실행 시에는 ignition 단계에서 검사하지만,
			// 예시의 불일치는 여기서 미리 잡습니다.
			if ps.Network.Source == config.NetworkGenerate {
				nics, err := csvdata.LoadNICs(ps.NICsCSV())
				if err != nil {
					t.Fatalf("예시 nics.csv 로드 실패: %v", err)
				}
				if err := csvdata.ValidateNICSelection(nics, ps.Network.Bonding,
					ps.Network.ActiveNIC, ps.Network.Bond.Primary, ps.Network.Bond.Standby); err != nil {
					t.Errorf("예시 pathset의 NIC 선택이 nics.csv와 어긋납니다: %v", err)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("검사한 예시 pathset이 없습니다")
	}
}
