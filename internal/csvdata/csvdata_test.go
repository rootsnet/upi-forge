package csvdata_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/csvdata"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("테스트 파일을 만들지 못했습니다: %v", err)
	}
	return path
}

func TestLoadNodesAcceptsHeaderBOMAndCRLF(t *testing.T) {
	// BOM, CRLF, 헤더가 함께 있는 입력도 처리해야 합니다.
	path := write(t, "nodes.csv", "\ufeffhostname,ip\r\nworker1.mycluster.example.com,192.0.2.153\r\nworker2.mycluster.example.com,192.0.2.154\r\n")

	nodes, err := csvdata.LoadNodes(path)
	if err != nil {
		t.Fatalf("LoadNodes 실패: %v", err)
	}
	if got := len(nodes.Items); got != 2 {
		t.Fatalf("노드 수가 다릅니다: got=%d want=2", got)
	}
	if nodes.Items[0].Hostname != "worker1.mycluster.example.com" {
		t.Errorf("첫 노드 hostname이 다릅니다: %q", nodes.Items[0].Hostname)
	}
	if ip, ok := nodes.IP("worker2.mycluster.example.com"); !ok || ip != "192.0.2.154" {
		t.Errorf("IP 조회 결과가 다릅니다: %q, ok=%t", ip, ok)
	}
}

func TestLoadNodesWithoutHeader(t *testing.T) {
	path := write(t, "nodes.csv", "worker1.mycluster.example.com,192.0.2.153\n")
	nodes, err := csvdata.LoadNodes(path)
	if err != nil {
		t.Fatalf("헤더 없는 CSV도 허용해야 합니다: %v", err)
	}
	if len(nodes.Items) != 1 {
		t.Fatalf("노드 수가 다릅니다: %d", len(nodes.Items))
	}
}

func TestLoadNodesRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"세 열":         "worker1,192.0.2.1,extra\n",
		"hostname중복":  "worker1,192.0.2.1\nworker1,192.0.2.2\n",
		"IP중복":        "worker1,192.0.2.1\nworker2,192.0.2.1\n",
		"잘못된IP":       "worker1,192.0.2.300\n",
		"빈파일":         "\n\n",
		"잘못된hostname": "worker_1,192.0.2.1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := write(t, "nodes.csv", content)
			if _, err := csvdata.LoadNodes(path); err == nil {
				t.Errorf("오류를 반환해야 합니다: %q", content)
			}
		})
	}
}

func TestNodesSelectAndFrom(t *testing.T) {
	path := write(t, "nodes.csv", "a.example.com,10.0.0.1\nb.example.com,10.0.0.2\nc.example.com,10.0.0.3\n")
	nodes, err := csvdata.LoadNodes(path)
	if err != nil {
		t.Fatalf("LoadNodes 실패: %v", err)
	}

	all, err := nodes.Select()
	if err != nil || len(all) != 3 {
		t.Fatalf("인자가 없으면 전체를 반환해야 합니다: %v, %v", all, err)
	}
	picked, err := nodes.Select("c.example.com", "a.example.com")
	if err != nil {
		t.Fatalf("Select 실패: %v", err)
	}
	if picked[0] != "c.example.com" || picked[1] != "a.example.com" {
		t.Errorf("지정한 순서를 유지해야 합니다: %v", picked)
	}
	if _, err := nodes.Select("a.example.com", "a.example.com"); err == nil {
		t.Error("중복 지정은 오류여야 합니다")
	}
	if _, err := nodes.Select("zzz"); err == nil {
		t.Error("CSV에 없는 노드는 오류여야 합니다")
	}

	from, err := nodes.From("b.example.com")
	if err != nil {
		t.Fatalf("From 실패: %v", err)
	}
	if len(from) != 2 || from[0] != "b.example.com" {
		t.Errorf("--from은 지정 노드부터 끝까지여야 합니다: %v", from)
	}
	if _, err := nodes.From("zzz"); err == nil {
		t.Error("없는 hostname은 오류여야 합니다")
	}
}

func TestLoadNICs(t *testing.T) {
	path := write(t, "nics.csv", "\ufeffnic\neno1np0\neno2np1\n")
	nics, err := csvdata.LoadNICs(path)
	if err != nil {
		t.Fatalf("LoadNICs 실패: %v", err)
	}
	if len(nics.Items) != 2 || nics.Items[0] != "eno1np0" {
		t.Fatalf("NIC 목록이 다릅니다: %v", nics.Items)
	}
	if !nics.Has("eno2np1") || nics.Has("bond0") {
		t.Error("Has 결과가 잘못되었습니다")
	}

	if _, err := csvdata.LoadNICs(write(t, "dup.csv", "eth0\neth0\n")); err == nil {
		t.Error("NIC 중복은 오류여야 합니다")
	}
}

func TestValidateNICSelection(t *testing.T) {
	path := write(t, "nics.csv", "eno1\neno2\n")
	nics, err := csvdata.LoadNICs(path)
	if err != nil {
		t.Fatalf("LoadNICs 실패: %v", err)
	}
	if err := csvdata.ValidateNICSelection(nics, false, "eno1", "", ""); err != nil {
		t.Errorf("단일 NIC 검증이 실패했습니다: %v", err)
	}
	if err := csvdata.ValidateNICSelection(nics, false, "eno9", "", ""); err == nil {
		t.Error("nics.csv에 없는 activeNIC은 오류여야 합니다")
	}
	if err := csvdata.ValidateNICSelection(nics, true, "", "eno1", "eno2"); err != nil {
		t.Errorf("bond 검증이 실패했습니다: %v", err)
	}
	if err := csvdata.ValidateNICSelection(nics, true, "", "eno1", "eno9"); err == nil {
		t.Error("nics.csv에 없는 standby는 오류여야 합니다")
	}
}

func TestIDRACsMatchNodes(t *testing.T) {
	nodesPath := write(t, "nodes.csv", "a.example.com,10.0.0.1\nb.example.com,10.0.0.2\n")
	nodes, err := csvdata.LoadNodes(nodesPath)
	if err != nil {
		t.Fatalf("LoadNodes 실패: %v", err)
	}

	ok := write(t, "idracs.csv", "hostname,idrac_ip,idrac_id\na.example.com,198.51.100.110,root\nb.example.com,198.51.100.111,root\n")
	idracs, err := csvdata.LoadIDRACs(ok)
	if err != nil {
		t.Fatalf("LoadIDRACs 실패: %v", err)
	}
	if err := idracs.MatchNodes(nodes); err != nil {
		t.Errorf("일대일 대응 검증이 실패했습니다: %v", err)
	}
	if entry, found := idracs.Get("b.example.com"); !found || entry.Address != "198.51.100.111" {
		t.Errorf("Get 결과가 다릅니다: %+v", entry)
	}

	// 노드 하나가 빠지면 다른 장비를 부팅할 수 있으므로 반드시 오류여야 합니다.
	missing := write(t, "missing.csv", "a.example.com,198.51.100.110,root\n")
	partial, err := csvdata.LoadIDRACs(missing)
	if err != nil {
		t.Fatalf("LoadIDRACs 실패: %v", err)
	}
	if err := partial.MatchNodes(nodes); err == nil {
		t.Error("nodes.csv와 개수가 다르면 오류여야 합니다")
	} else if !strings.Contains(err.Error(), "b.example.com") {
		t.Errorf("빠진 노드 이름이 오류에 있어야 합니다: %v", err)
	}
}

func TestLoadIDRACsRejectsDuplicateAddress(t *testing.T) {
	path := write(t, "idracs.csv", "a.example.com,198.51.100.110,root\nb.example.com,198.51.100.110,root\n")
	if _, err := csvdata.LoadIDRACs(path); err == nil {
		t.Error("iDRAC IP 중복은 오류여야 합니다")
	}
}
