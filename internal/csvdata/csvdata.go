// Package csvdata는 pathset의 nodes.csv, nics.csv, idracs.csv를 읽고
// 일관된 규칙으로 검증합니다.
//
//   - UTF-8 BOM과 CRLF 줄바꿈을 허용합니다.
//   - 헤더 행은 선택 사항이며, 쓰려면 정확한 이름이어야 합니다.
//   - 열 개수가 정확히 맞아야 합니다. 남는 열은 오류입니다.
//   - hostname, IP, NIC 이름의 중복은 오류입니다.
//   - 데이터 행이 하나도 없으면 오류입니다.
//   - 파일의 행 순서가 그대로 처리 순서가 됩니다.
package csvdata

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// hostnamePattern은 hostname 또는 FQDN 형식을 검사합니다.
var hostnamePattern = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// nicPattern은 NIC 이름 형식을 검사합니다.
var nicPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// Node는 nodes.csv 한 행입니다.
type Node struct {
	Hostname string
	IP       string
}

// Nodes는 nodes.csv 전체를 파일 순서대로 담습니다.
type Nodes struct {
	Path  string
	Items []Node

	index map[string]int
}

// IDRAC은 idracs.csv 한 행입니다. 비밀번호는 파일에 저장하지 않습니다.
type IDRAC struct {
	Hostname string
	Address  string
	Username string
}

// IDRACs는 idracs.csv 전체를 담습니다.
type IDRACs struct {
	Path  string
	Items []IDRAC

	index map[string]int
}

// NICs는 nics.csv의 NIC 이름 목록을 파일 순서대로 담습니다.
type NICs struct {
	Path  string
	Items []string
}

// record는 한 줄을 파싱한 결과입니다.
type record struct {
	line   int
	fields []string
}

// readRecords는 파일을 읽어 빈 줄을 제외한 행 목록을 만듭니다.
// 열 개수가 want와 다르면 오류입니다.
func readRecords(path string, want int, columns string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("파일을 읽을 수 없습니다: %s: %w", path, err)
	}
	defer f.Close()

	var out []record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\uFEFF") // UTF-8 BOM
		}
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != want {
			return nil, fmt.Errorf("%s %d행: 정확히 %d열(%s)이어야 합니다", path, lineNo, want, columns)
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		out = append(out, record{line: lineNo, fields: fields})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("파일을 읽는 중 오류가 발생했습니다: %s: %w", path, err)
	}
	return out, nil
}

// isHeader는 첫 데이터 행 이전에 나온 헤더인지 확인합니다.
func isHeader(fields, header []string) bool {
	if len(fields) != len(header) {
		return false
	}
	for i := range fields {
		if fields[i] != header[i] {
			return false
		}
	}
	return true
}

// containsHeaderWord는 헤더 단어가 데이터 행에 섞여 들어왔는지 확인합니다.
func containsHeaderWord(fields, header []string) bool {
	for i := range fields {
		if fields[i] == header[i] {
			return true
		}
	}
	return false
}

// validIPv4는 점 4개 형식의 IPv4 주소인지 확인합니다.
func validIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && strings.Count(s, ".") == 3
}

// LoadNodes는 hostname,ip 두 열 CSV를 읽습니다.
func LoadNodes(path string) (*Nodes, error) {
	header := []string{"hostname", "ip"}
	records, err := readRecords(path, 2, "hostname,ip")
	if err != nil {
		return nil, err
	}

	nodes := &Nodes{Path: path, index: map[string]int{}}
	seenIP := map[string]string{}
	for i, rec := range records {
		host, ip := rec.fields[0], rec.fields[1]
		if i == 0 && isHeader(rec.fields, header) {
			continue
		}
		if containsHeaderWord(rec.fields, header) {
			return nil, fmt.Errorf("%s %d행: 헤더를 사용하려면 첫 행에 정확히 hostname,ip로 작성하세요", path, rec.line)
		}
		if host == "" || ip == "" {
			return nil, fmt.Errorf("%s %d행: hostname과 ip는 필수입니다", path, rec.line)
		}
		if !hostnamePattern.MatchString(host) {
			return nil, fmt.Errorf("%s %d행: hostname은 유효한 hostname 또는 FQDN이어야 합니다: %s", path, rec.line, host)
		}
		if !validIPv4(ip) {
			return nil, fmt.Errorf("%s %d행: 유효하지 않은 IPv4 주소입니다: %s", path, rec.line, ip)
		}
		if _, dup := nodes.index[host]; dup {
			return nil, fmt.Errorf("%s %d행: hostname 중복: %s", path, rec.line, host)
		}
		if prev, dup := seenIP[ip]; dup {
			return nil, fmt.Errorf("%s %d행: IP 중복: %s (이미 %s에서 사용)", path, rec.line, ip, prev)
		}
		nodes.index[host] = len(nodes.Items)
		nodes.Items = append(nodes.Items, Node{Hostname: host, IP: ip})
		seenIP[ip] = host
	}
	if len(nodes.Items) == 0 {
		return nil, fmt.Errorf("%s: 노드 데이터 행이 없습니다", path)
	}
	return nodes, nil
}

// Hostnames는 파일 순서대로 hostname 목록을 반환합니다.
func (n *Nodes) Hostnames() []string {
	out := make([]string, 0, len(n.Items))
	for _, item := range n.Items {
		out = append(out, item.Hostname)
	}
	return out
}

// IP는 hostname에 대응하는 IP를 반환합니다.
func (n *Nodes) IP(hostname string) (string, bool) {
	i, ok := n.index[hostname]
	if !ok {
		return "", false
	}
	return n.Items[i].IP, true
}

// Has는 hostname이 CSV에 있는지 확인합니다.
func (n *Nodes) Has(hostname string) bool {
	_, ok := n.index[hostname]
	return ok
}

// Select는 인자로 받은 노드만 골라 파일 순서와 무관하게 지정 순서대로 반환합니다.
// 인자가 없으면 파일 순서대로 모든 노드를 반환합니다.
func (n *Nodes) Select(names ...string) ([]string, error) {
	if len(names) == 0 {
		return n.Hostnames(), nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !n.Has(name) {
			return nil, fmt.Errorf("%s: CSV에 없는 노드가 지정되었습니다: %s", n.Path, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: 같은 노드를 중복 지정할 수 없습니다: %s", n.Path, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// From은 지정한 hostname부터 파일의 마지막 노드까지를 반환합니다.
// 빈 문자열이면 전체 노드를 반환합니다.
func (n *Nodes) From(start string) ([]string, error) {
	all := n.Hostnames()
	if start == "" {
		return all, nil
	}
	for i, host := range all {
		if host == start {
			return all[i:], nil
		}
	}
	return nil, fmt.Errorf("%s: --from으로 지정한 hostname이 없습니다: %s", n.Path, start)
}

// LoadNICs는 NIC 이름을 한 줄에 하나씩 적은 CSV를 읽습니다.
func LoadNICs(path string) (*NICs, error) {
	records, err := readRecords(path, 1, "nic")
	if err != nil {
		return nil, err
	}

	nics := &NICs{Path: path}
	seen := map[string]bool{}
	for i, rec := range records {
		name := rec.fields[0]
		if i == 0 && name == "nic" {
			continue
		}
		if name == "" {
			continue
		}
		if !nicPattern.MatchString(name) {
			return nil, fmt.Errorf("%s %d행: 올바르지 않은 NIC 이름입니다: %s", path, rec.line, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s %d행: NIC 이름 중복: %s", path, rec.line, name)
		}
		seen[name] = true
		nics.Items = append(nics.Items, name)
	}
	if len(nics.Items) == 0 {
		return nil, fmt.Errorf("%s: NIC 데이터 행이 없습니다", path)
	}
	return nics, nil
}

// Has는 NIC 이름이 목록에 있는지 확인합니다.
func (n *NICs) Has(name string) bool {
	for _, item := range n.Items {
		if item == name {
			return true
		}
	}
	return false
}

// LoadIDRACs는 hostname,idrac_ip,idrac_id 세 열 CSV를 읽습니다.
func LoadIDRACs(path string) (*IDRACs, error) {
	header := []string{"hostname", "idrac_ip", "idrac_id"}
	records, err := readRecords(path, 3, "hostname,idrac_ip,idrac_id")
	if err != nil {
		return nil, err
	}

	idracs := &IDRACs{Path: path, index: map[string]int{}}
	seenAddr := map[string]string{}
	for i, rec := range records {
		host, addr, user := rec.fields[0], rec.fields[1], rec.fields[2]
		if i == 0 && isHeader(rec.fields, header) {
			continue
		}
		if containsHeaderWord(rec.fields, header) {
			return nil, fmt.Errorf("%s %d행: 헤더를 사용하려면 첫 행에 정확히 hostname,idrac_ip,idrac_id로 작성하세요", path, rec.line)
		}
		if host == "" || addr == "" || user == "" {
			return nil, fmt.Errorf("%s %d행: hostname, idrac_ip, idrac_id는 모두 필수입니다", path, rec.line)
		}
		if !validIPv4(addr) {
			return nil, fmt.Errorf("%s %d행: 유효하지 않은 iDRAC IP입니다: %s", path, rec.line, addr)
		}
		if _, dup := idracs.index[host]; dup {
			return nil, fmt.Errorf("%s %d행: hostname 중복: %s", path, rec.line, host)
		}
		if prev, dup := seenAddr[addr]; dup {
			return nil, fmt.Errorf("%s %d행: iDRAC IP 중복: %s (이미 %s에서 사용)", path, rec.line, addr, prev)
		}
		idracs.index[host] = len(idracs.Items)
		idracs.Items = append(idracs.Items, IDRAC{Hostname: host, Address: addr, Username: user})
		seenAddr[addr] = host
	}
	if len(idracs.Items) == 0 {
		return nil, fmt.Errorf("%s: iDRAC 데이터 행이 없습니다", path)
	}
	return idracs, nil
}

// Get은 hostname에 대응하는 iDRAC 정보를 반환합니다.
func (d *IDRACs) Get(hostname string) (IDRAC, bool) {
	i, ok := d.index[hostname]
	if !ok {
		return IDRAC{}, false
	}
	return d.Items[i], true
}

// MatchNodes는 nodes.csv와 idracs.csv가 hostname 기준으로 일대일 대응하는지 확인합니다.
// 다른 장비의 설정을 사용하지 않도록 두 파일의 노드 구성을 함께 확인합니다.
func (d *IDRACs) MatchNodes(nodes *Nodes) error {
	for _, item := range d.Items {
		if !nodes.Has(item.Hostname) {
			return fmt.Errorf("%s: nodes.csv에 없는 hostname입니다: %s", d.Path, item.Hostname)
		}
	}
	for _, node := range nodes.Items {
		if _, ok := d.Get(node.Hostname); !ok {
			return fmt.Errorf("%s: iDRAC 정보가 없는 노드입니다: %s", d.Path, node.Hostname)
		}
	}
	if len(d.Items) != len(nodes.Items) {
		return fmt.Errorf("nodes.csv(%d개)와 idracs.csv(%d개)의 노드 수가 다릅니다", len(nodes.Items), len(d.Items))
	}
	return nil
}

// ValidateNICSelection은 pathset의 NIC 선택이 nics.csv와 맞는지 확인합니다.
// 선택한 NIC가 목록에 정확히 한 번씩 있는지도 확인합니다.
func ValidateNICSelection(nics *NICs, bonding bool, activeNIC, bondPrimary, bondStandby string) error {
	if bonding {
		if !nics.Has(bondPrimary) {
			return fmt.Errorf("%s: bond.primary가 nics.csv에 없습니다: %s", nics.Path, bondPrimary)
		}
		if !nics.Has(bondStandby) {
			return fmt.Errorf("%s: bond.standby가 nics.csv에 없습니다: %s", nics.Path, bondStandby)
		}
		return nil
	}
	if !nics.Has(activeNIC) {
		return fmt.Errorf("%s: network.activeNIC이 nics.csv에 없습니다: %s", nics.Path, activeNIC)
	}
	return nil
}
