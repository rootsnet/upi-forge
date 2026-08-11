package yamlx_test

import (
	"strings"
	"testing"

	"upi-forge/internal/yamlx"
)

func TestParseNestedMappingAndSequences(t *testing.T) {
	doc := `
# 주석은 무시합니다
cluster:
  domain: "mycluster.example.com"
  apiURL: https://api.mycluster.example.com:6443   # 값 안의 콜론
  mcsPort: 22623
network:
  dns:
    - 192.0.2.7
    - 192.0.2.6
  flow: [a, b, "c, still c"]
  bonding: true
adapters:
  - id: NIC.Slot.5
    ports:
      - name: p1
      - name: p2
  - id: NIC.Slot.6
empty:
`
	root, err := yamlx.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	var domain, apiURL, missing string
	var mcsPort int
	var bonding bool
	var dns, flow []string

	m := yamlx.NewMapper(root)
	cluster := m.Section("cluster")
	cluster.String("domain", &domain)
	cluster.String("apiURL", &apiURL)
	cluster.Int("mcsPort", &mcsPort)

	network := m.Section("network")
	network.Strings("dns", &dns)
	network.Strings("flow", &flow)
	network.Bool("bonding", &bonding)

	// 접근하지 않은 키가 있으면 Finish에서 오류가 나므로 모두 소비합니다.
	m.String("empty", &missing)
	adapters := m.Node("adapters")

	if err := m.Finish(); err != nil {
		t.Fatalf("Finish 실패: %v", err)
	}

	if domain != "mycluster.example.com" {
		t.Errorf("domain: %q", domain)
	}
	if apiURL != "https://api.mycluster.example.com:6443" {
		t.Errorf("apiURL: %q", apiURL)
	}
	if mcsPort != 22623 {
		t.Errorf("mcsPort: %d", mcsPort)
	}
	if !bonding {
		t.Error("bonding이 true여야 합니다")
	}
	if len(dns) != 2 || dns[0] != "192.0.2.7" {
		t.Errorf("dns: %v", dns)
	}
	if len(flow) != 3 || flow[2] != "c, still c" {
		t.Errorf("flow: %v", flow)
	}

	if adapters == nil || adapters.Kind != yamlx.KindSequence || len(adapters.Items) != 2 {
		t.Fatalf("adapters 시퀀스가 잘못되었습니다: %+v", adapters)
	}
	first := adapters.Items[0]
	if first.Kind != yamlx.KindMapping || first.Fields["id"].Value != "NIC.Slot.5" {
		t.Fatalf("시퀀스 안의 매핑이 잘못되었습니다: %+v", first)
	}
	ports := first.Fields["ports"]
	if ports == nil || len(ports.Items) != 2 || ports.Items[1].Fields["name"].Value != "p2" {
		t.Fatalf("중첩 시퀀스가 잘못되었습니다: %+v", ports)
	}
}

func TestMapperReportsUnknownKey(t *testing.T) {
	root, err := yamlx.Parse([]byte("cluster:\n  domain: a\n  domian: b\n"))
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}
	var domain string
	m := yamlx.NewMapper(root)
	m.Section("cluster").String("domain", &domain)

	err = m.Finish()
	if err == nil {
		t.Fatal("오타 난 키는 오류로 보고해야 합니다")
	}
	if !strings.Contains(err.Error(), "domian") {
		t.Errorf("오류 메시지에 오타 키가 있어야 합니다: %v", err)
	}
}

func TestMapperTypeErrors(t *testing.T) {
	root, err := yamlx.Parse([]byte("port: abc\nflag: maybe\n"))
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}
	var port int
	var flag bool
	m := yamlx.NewMapper(root)
	m.Int("port", &port)
	m.Bool("flag", &flag)

	if err := m.Finish(); err == nil {
		t.Error("잘못된 타입은 오류여야 합니다")
	}
}

func TestParseRejectsUnsupportedSyntax(t *testing.T) {
	cases := map[string]string{
		"블록 스칼라": "key: |\n  line\n",
		"앵커":     "key: &anchor value\n",
		"한 줄 매핑": "key: {a: 1}\n",
		"여러 문서":  "---\nkey: value\n",
		"탭 들여쓰기": "key:\n\tnested: value\n",
		"키 없음":   "just a string\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := yamlx.Parse([]byte(doc)); err == nil {
				t.Errorf("지원하지 않는 문법은 오류여야 합니다: %q", doc)
			}
		})
	}
}

func TestParseRejectsDuplicateKey(t *testing.T) {
	if _, err := yamlx.Parse([]byte("a: 1\na: 2\n")); err == nil {
		t.Error("키 중복은 오류여야 합니다")
	}
}

func TestQuoteAndNumber(t *testing.T) {
	if got := yamlx.Quote(`he said "hi"`); got != `"he said \"hi\""` {
		t.Errorf("Quote: %s", got)
	}
	if got := yamlx.Number(nil); got != "null" {
		t.Errorf("Number(nil): %s", got)
	}
	v := 10000
	if got := yamlx.Number(&v); got != "10000" {
		t.Errorf("Number: %s", got)
	}
	// 제어 문자는 YAML 이스케이프로 바뀌어야 합니다. 이스케이프 없이
	// 그대로 출력되면 큰따옴표 스칼라가 깨집니다.
	if got := yamlx.Quote("a\rb\x00c\x7fd"); got != "\"a\\u000Db\\u0000c\\u007Fd\"" {
		t.Errorf("Quote(제어 문자): %s", got)
	}
	// DEL/C1 문자와 비문자도 원문으로 출력하지 않습니다. U+0085(NEL)는
	// YAML 1.2가 허용하지만 개행류 문자이므로 함께 이스케이프합니다.
	if got := yamlx.Quote("a\u0085b\u009fc\ufffed"); got != "\"a\\u0085b\\u009Fc\\uFFFEd\"" {
		t.Errorf("Quote(C1·비문자): %s", got)
	}
	if got := yamlx.Quote("줄\n탭\t끝"); got != `"줄\n탭\t끝"` {
		t.Errorf("Quote(개행·탭): %s", got)
	}
}
