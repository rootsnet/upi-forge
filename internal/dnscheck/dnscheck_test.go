package dnscheck

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// fakeQuerier는 서버 하나의 정방향/역방향 응답을 맵으로 재현합니다.
// 맵에 없는 이름은 "레코드 없음"(errNotFound) 응답입니다.
type fakeQuerier struct {
	forward map[string][]string // fqdn -> IP 목록
	reverse map[string][]string // ip -> PTR 이름 목록(끝 점 포함 가능)
	err     error               // 설정하면 모든 조회가 이 오류로 실패
	ptrErr  error               // 설정하면 역방향 조회만 이 오류로 실패
	aCalls  int
	ptrCall int
}

func (f *fakeQuerier) QueryA(_ context.Context, host string) ([]net.IP, error) {
	f.aCalls++
	if f.err != nil {
		return nil, f.err
	}
	ips, ok := f.forward[host]
	if !ok {
		return nil, errNotFound
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.ParseIP(ip))
	}
	return out, nil
}

func (f *fakeQuerier) QueryPTR(_ context.Context, addr string) ([]string, error) {
	f.ptrCall++
	if f.err != nil {
		return nil, f.err
	}
	if f.ptrErr != nil {
		return nil, f.ptrErr
	}
	names, ok := f.reverse[addr]
	if !ok {
		return nil, errNotFound
	}
	return names, nil
}

func checkerWith(qs ...Querier) *Checker {
	servers := make([]Server, 0, len(qs))
	for i, q := range qs {
		servers = append(servers, Server{Address: "192.0.2." + string(rune('7'-i)), Querier: q})
	}
	return &Checker{Servers: servers, Domain: "mycluster.example.com"}
}

func TestFQDN(t *testing.T) {
	cases := []struct {
		host, want string
		derived    bool
	}{
		{"worker1", "worker1.mycluster.example.com", true},
		{"worker2.other.example.com", "worker2.other.example.com", false},
	}
	for _, c := range cases {
		got, derived := FQDN(c.host, "mycluster.example.com")
		if got != c.want || derived != c.derived {
			t.Errorf("FQDN(%q) = %q, %v; want %q, %v", c.host, got, derived, c.want, c.derived)
		}
	}
}

// FQDN hostname은 클러스터 도메인에 속해야 합니다.
func TestCheckHostnameDomain(t *testing.T) {
	domain := "mycluster.example.com"
	pass := []string{
		"worker1",                           // 짧은 이름: 도메인이 붙으므로 통과
		"worker2.mycluster.example.com",     // 일치
		"Worker2.MYCLUSTER.EXAMPLE.COM",     // 대소문자 무시
		"extra.depth.mycluster.example.com", // 더 깊은 하위 도메인도 소속으로 인정
	}
	for _, host := range pass {
		if err := CheckHostnameDomain(host, domain); err != nil {
			t.Errorf("%s는 통과해야 합니다: %v", host, err)
		}
	}
	fail := []string{
		"worker2.other.example.com",     // 다른 클러스터 도메인의 FQDN
		"worker2.example.com",           // 상위 도메인만 일치
		"mycluster.example.com",         // 호스트 라벨 없이 도메인만
		"worker2.mycluster.example.org", // 다른 TLD
	}
	for _, host := range fail {
		err := CheckHostnameDomain(host, domain)
		if err == nil || !strings.Contains(err.Error(), "클러스터 도메인과 다릅니다") {
			t.Errorf("%s는 도메인 불일치 오류여야 합니다: %v", host, err)
		}
	}
}

// 정방향 일치 + 역방향 일치면 통과합니다.
func TestCheckPasses(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"198.51.100.154"}},
		reverse: map[string][]string{"198.51.100.154": {"WORKER1.mycluster.example.com."}},
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "198.51.100.154"}})
	if err != nil {
		t.Fatalf("통과해야 하는데 실패: %v", err)
	}
}

// FQDN이 아닌 hostname은 cluster.domain을 붙인 이름으로 조회해야 합니다.
// (도메인을 붙이지 않으면 fake 맵에 없어 실패하므로, 이 테스트가
// 도메인 결합 동작 자체를 고정합니다.)
func TestCheckShortHostnameUsesClusterDomain(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err != nil {
		t.Fatalf("임시 FQDN 조회가 실패했습니다: %v", err)
	}
}

// 정방향 레코드가 없으면 오류입니다.
func TestCheckForwardMissingFails(t *testing.T) {
	fake := &fakeQuerier{forward: map[string][]string{}}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "정방향 실패") {
		t.Fatalf("정방향 레코드 없음 오류가 나와야 합니다: %v", err)
	}
}

// 정방향 조회 결과에 기대 IP가 없으면 오류입니다.
func TestCheckForwardMismatchFails(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.99"}},
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "정방향 불일치") {
		t.Fatalf("정방향 불일치 오류가 나와야 합니다: %v", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.99") {
		t.Fatalf("실제 조회된 IP가 오류에 있어야 합니다: %v", err)
	}
}

// PTR 레코드가 없으면 오류 없이 건너뜁니다.
func TestCheckReverseMissingSkips(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
		reverse: map[string][]string{}, // PTR 없음
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err != nil {
		t.Fatalf("PTR 없음은 건너뛰어야 합니다: %v", err)
	}
}

// PTR 레코드가 있는데 FQDN과 다르면 오류입니다.
func TestCheckReverseMismatchFails(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
		reverse: map[string][]string{"10.0.0.1": {"other.example.com."}},
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "역방향 불일치") {
		t.Fatalf("역방향 불일치 오류가 나와야 합니다: %v", err)
	}
	if !strings.Contains(err.Error(), "other.example.com") {
		t.Fatalf("실제 PTR 이름이 오류에 있어야 합니다: %v", err)
	}
}

// 회귀 테스트: 첫 서버에 PTR이 없어도 멈추지 않고
// 나머지 서버를 검사해, 다른 서버의 잘못된 PTR을 잡아야 합니다.
func TestCheckWrongPTROnSecondServerFails(t *testing.T) {
	first := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
		reverse: map[string][]string{}, // PTR 없음 → 이 서버는 건너뜀
	}
	second := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
		reverse: map[string][]string{"10.0.0.1": {"wrong.example.com."}},
	}
	err := checkerWith(first, second).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "역방향 불일치") {
		t.Fatalf("두 번째 서버의 잘못된 PTR을 잡아야 합니다: %v", err)
	}
}

// 정방향도 응답하는 모든 서버를 검사합니다. 한 서버만 잘못된 IP를
// 반환해도 노드가 그 서버를 쓸 때 문제가 되므로 오류입니다.
func TestCheckWrongForwardOnSecondServerFails(t *testing.T) {
	first := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
	}
	second := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.99"}},
	}
	err := checkerWith(first, second).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "정방향 불일치") {
		t.Fatalf("두 번째 서버의 잘못된 A 레코드를 잡아야 합니다: %v", err)
	}
}

// 서버 간 정방향 결과가 다르면(한쪽만 레코드 있음) 오류입니다.
func TestCheckInconsistentForwardFails(t *testing.T) {
	has := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
	}
	missing := &fakeQuerier{forward: map[string][]string{}}
	err := checkerWith(has, missing).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "레코드가 없습니다") {
		t.Fatalf("서버 간 불일치를 잡아야 합니다: %v", err)
	}
}

// 회귀 테스트: 서버가 오류 코드(SERVFAIL 등)로
// 응답하면 접속 실패처럼 조용히 제외하지 말고 검증 실패로 다뤄야 합니다.
// (DNS1이 SERVFAIL, DNS2가 올바른 A를 줘도 노드가 DNS1을 쓰면 해석에
// 실패하므로 통과시키면 안 됩니다.)
func TestCheckForwardServFailIsErrorNotSkip(t *testing.T) {
	servfail := &fakeQuerier{err: &rcodeError{server: "192.0.2.7:53", rcode: 2}}
	good := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
	}
	err := checkerWith(servfail, good).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "SERVFAIL") {
		t.Fatalf("SERVFAIL 서버가 있으면 실패해야 합니다: %v", err)
	}
	if !strings.Contains(err.Error(), "정방향 실패") {
		t.Fatalf("정방향 검증 실패로 보고되어야 합니다: %v", err)
	}
}

// 회귀 테스트: 해석할 수 없는 응답(프로토콜
// 오류)도 접속 실패가 아니라 비정상 응답이므로 정방향 검증 실패입니다.
func TestCheckForwardMalformedResponseFails(t *testing.T) {
	broken := &fakeQuerier{err: &protocolError{
		server: "192.0.2.7:53", reason: errors.New("DNS 응답이 너무 짧습니다")}}
	good := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
	}
	err := checkerWith(broken, good).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "정방향 실패") ||
		!strings.Contains(err.Error(), "올바르지 않습니다") {
		t.Fatalf("깨진 응답 서버가 있으면 실패해야 합니다: %v", err)
	}
}

// 역방향의 비정상 응답(프로토콜 오류 포함)은 오류가 아니라 "PTR 없음"으로
// 간주하고 경고 후 건너뜁니다.
func TestCheckReverseMalformedResponseSkips(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
		ptrErr: &protocolError{
			server: "192.0.2.7:53", reason: errors.New("DNS 응답이 아닙니다(QR=0)")},
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err != nil {
		t.Fatalf("역방향 프로토콜 오류는 PTR 없음처럼 건너뛰어야 합니다: %v", err)
	}
}

// 역방향 존이 없는 DNS 서버는 NXDOMAIN 대신 SERVFAIL을 반환할 수 있으므로,
// 역방향 서버 오류는 경고 후 건너뜁니다.
func TestCheckReverseServFailSkipsWithWarning(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
		ptrErr:  &rcodeError{server: "192.0.2.7:53", rcode: 2},
	}
	err := checkerWith(fake).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err != nil {
		t.Fatalf("역방향 SERVFAIL은 PTR 없음처럼 건너뛰어야 합니다: %v", err)
	}
}

// 역방향 조회의 접속 오류는 경고 후 건너뛰되, 같은 서버의 필수 정방향
// 검증에는 계속 사용해야 합니다.
func TestCheckReverseNetErrorSkipsAndKeepsForward(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{
			"worker1.mycluster.example.com": {"10.0.0.1"},
			"worker2.mycluster.example.com": {"10.0.0.2"},
		},
		ptrErr: errors.New("i/o timeout"), // 역방향만 접속 오류
	}
	err := checkerWith(fake).Check(context.Background(), []Node{
		{Hostname: "worker1", IP: "10.0.0.1"},
		{Hostname: "worker2", IP: "10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("역방향 접속 오류는 건너뛰고 정방향은 계속되어야 합니다: %v", err)
	}
	if fake.aCalls != 2 {
		t.Fatalf("두 노드의 정방향이 모두 검증되어야 합니다(역방향 오류로 서버가 제외되면 안 됨): %d회", fake.aCalls)
	}
}

// 접속 실패한 서버는 건너뛰고 나머지 서버로 검증을 계속합니다.
func TestCheckFallsBackToNextServer(t *testing.T) {
	broken := &fakeQuerier{err: errors.New("connection refused")}
	good := &fakeQuerier{
		forward: map[string][]string{"worker1.mycluster.example.com": {"10.0.0.1"}},
	}
	err := checkerWith(broken, good).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err != nil {
		t.Fatalf("두 번째 서버로 통과해야 합니다: %v", err)
	}
}

// 접속 실패한 서버는 한 번만 시도하고 이후 노드에서는 기다리지 않습니다.
// (죽은 서버 하나 때문에 노드마다 제한 시간을 소모하지 않도록)
func TestCheckDownServerNotRetriedPerNode(t *testing.T) {
	broken := &fakeQuerier{err: errors.New("i/o timeout")}
	good := &fakeQuerier{
		forward: map[string][]string{
			"worker1.mycluster.example.com": {"10.0.0.1"},
			"worker2.mycluster.example.com": {"10.0.0.2"},
		},
	}
	err := checkerWith(broken, good).Check(context.Background(), []Node{
		{Hostname: "worker1", IP: "10.0.0.1"},
		{Hostname: "worker2", IP: "10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("정상 서버로 통과해야 합니다: %v", err)
	}
	if total := broken.aCalls + broken.ptrCall; total != 1 {
		t.Fatalf("죽은 서버는 1회만 시도해야 합니다: %d회 시도됨", total)
	}
}

// 모든 서버가 접속 실패면 실패합니다.
func TestCheckAllServersUnreachableFails(t *testing.T) {
	broken := &fakeQuerier{err: errors.New("connection refused")}
	err := checkerWith(broken).Check(context.Background(),
		[]Node{{Hostname: "worker1", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "응답한 DNS 서버가 없습니다") {
		t.Fatalf("모든 서버 실패 오류가 나와야 합니다: %v", err)
	}
}

// 여러 노드의 실패를 모두 모아 한 번에 보고합니다.
func TestCheckCollectsAllFailures(t *testing.T) {
	fake := &fakeQuerier{
		forward: map[string][]string{
			"worker1.mycluster.example.com": {"10.0.0.99"}, // 불일치
			// worker2는 레코드 없음
		},
	}
	err := checkerWith(fake).Check(context.Background(), []Node{
		{Hostname: "worker1", IP: "10.0.0.1"},
		{Hostname: "worker2", IP: "10.0.0.2"},
	})
	if err == nil {
		t.Fatal("두 노드 모두 실패해야 합니다")
	}
	msg := err.Error()
	if !strings.Contains(msg, "worker1.mycluster.example.com") || !strings.Contains(msg, "worker2.mycluster.example.com") {
		t.Fatalf("두 노드의 실패가 모두 보고되어야 합니다: %v", err)
	}
	if !strings.Contains(msg, "2건") {
		t.Fatalf("실패 건수가 요약되어야 합니다: %v", err)
	}
}

// 서버 목록이 비어 있으면 설정 오류로 실패합니다.
// (generate 모드는 pathset 검증이 network.dns를 필수로 요구하고,
// copy 모드는 cli가 이 함수를 호출하기 전에 건너뛰므로 실제로는 도달하지
// 않지만, 잘못 호출됐을 때 조용히 통과하지 않도록 고정합니다.)
func TestCheckNoServersFails(t *testing.T) {
	c := &Checker{Domain: "mycluster.example.com"}
	err := c.Check(context.Background(), []Node{{Hostname: "w", IP: "10.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "network.dns") {
		t.Fatalf("서버 없음은 설정 오류여야 합니다: %v", err)
	}
}

// NewServers는 주소마다 와이어 클라이언트를 하나씩 만듭니다.
func TestNewServers(t *testing.T) {
	servers := NewServers([]string{"192.0.2.7", "192.0.2.6"})
	if len(servers) != 2 {
		t.Fatalf("서버 2개가 만들어져야 합니다: %d", len(servers))
	}
	for i, addr := range []string{"192.0.2.7", "192.0.2.6"} {
		if servers[i].Address != addr {
			t.Errorf("서버 %d 주소: %s != %s", i, servers[i].Address, addr)
		}
		c, ok := servers[i].Querier.(*client)
		if !ok || c.server != addr+":53" {
			t.Errorf("서버 %d는 53번 포트의 와이어 클라이언트여야 합니다: %+v", i, servers[i].Querier)
		}
	}
}
