// Package dnscheck는 ignition 단계 전에 노드 hostname의 DNS 등록을 검증합니다.
//
// 검증 대상 FQDN은 다음 규칙으로 정합니다.
//
//   - nodes.csv의 hostname이 FQDN이면(점 포함) 그대로 사용합니다.
//   - FQDN이 아니면 hostname + "." + cluster.domain(upi-forge.yaml)으로
//     임시 FQDN을 만들어 사용합니다.
//
// 검증 내용:
//
//   - 정방향(A): FQDN을 조회해 nodes.csv의 IP가 나와야 합니다.
//     레코드가 없거나 다른 IP만 나오면 오류입니다.
//   - 역방향(PTR): IP를 조회해 PTR이 있으면 FQDN과 일치해야 합니다.
//     PTR 레코드가 없거나 확인할 수 없으면 경고 후 건너뜁니다.
//
// 조회는 pathset의 network.dns 서버로 직접 보냅니다(client.go의 자체
// 와이어 클라이언트 — /etc/hosts와 search 도메인의 영향을 받지 않습니다).
// 노드는 resolv.conf 순서와 상태에 따라 목록의 어느 서버든 사용할 수
// 있으므로, 응답하는 모든 서버를 검사합니다.
// 접속되지 않는 서버는 경고 후 제외하고, 한 서버라도 정방향에 응답해야
// 노드 검증이 성립합니다.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"upi-forge/internal/logx"
)

// defaultTimeout은 질의당 제한 시간의 기본값입니다.
const defaultTimeout = 5 * time.Second

// Querier는 DNS 서버 하나에 대한 조회 두 가지입니다.
// 실제 구현은 client.go에 있고, 테스트에서는 테스트 대역을 주입합니다.
type Querier interface {
	// QueryA는 A 레코드 IP 목록을 반환합니다. 레코드가 없으면 errNotFound.
	QueryA(ctx context.Context, fqdn string) ([]net.IP, error)
	// QueryPTR은 PTR 이름 목록을 반환합니다. 레코드가 없으면 errNotFound.
	QueryPTR(ctx context.Context, ip string) ([]string, error)
}

// Server는 조회에 사용할 DNS 서버 하나입니다.
type Server struct {
	Address string // 로그와 오류 메시지에 쓰는 서버 주소
	Querier Querier
}

// NewServers는 pathset의 DNS 서버 주소들로 조회기를 만듭니다.
func NewServers(addrs []string) []Server {
	out := make([]Server, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, Server{
			Address: addr,
			Querier: &client{server: net.JoinHostPort(addr, "53")},
		})
	}
	return out
}

// Node는 검증 대상 한 노드입니다. nodes.csv 한 행에 해당합니다.
type Node struct {
	Hostname string
	IP       string
}

// FQDN은 hostname이 이미 FQDN이면 그대로, 아니면 hostname.domain을 반환합니다.
// 두 번째 반환값은 도메인을 붙여 임시 FQDN을 만들었는지 여부입니다.
func FQDN(hostname, domain string) (string, bool) {
	if strings.Contains(hostname, ".") {
		return hostname, false
	}
	return hostname + "." + domain, true
}

// CheckHostnameDomain은 FQDN hostname이 클러스터 도메인 소속인지 확인합니다.
// FQDN이 아닌 hostname은 클러스터 도메인이 붙으므로 항상 통과합니다.
//
// 다른 클러스터의 FQDN이 nodes.csv에 들어 있어도 그 클러스터의 DNS에
// 정상 등록돼 있으면 정방향 조회만으로는 잘못된 설정을 구분할 수 없습니다.
// 따라서 hostname의 도메인이 cluster.domain과 다르면 진행을 중단합니다.
func CheckHostnameDomain(hostname, domain string) error {
	if !strings.Contains(hostname, ".") {
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(hostname), "."+strings.ToLower(domain)) {
		return fmt.Errorf(
			"노드 hostname의 도메인이 클러스터 도메인과 다릅니다: %s (cluster.domain: %s) — 다른 클러스터의 nodes.csv이거나 cluster.domain 설정이 잘못됐을 수 있습니다",
			hostname, domain)
	}
	return nil
}

// Checker는 노드 목록의 DNS 등록을 검증합니다.
type Checker struct {
	Servers []Server
	Domain  string        // cluster.domain — FQDN이 아닌 hostname에 붙입니다.
	Timeout time.Duration // 질의당 제한 시간. 0이면 defaultTimeout.

	// down은 접속에 실패한 서버입니다. 노드가 많을 때 죽은 서버를
	// 노드마다 다시 기다리지 않도록 한 번 실패한 서버는 제외합니다.
	down map[string]bool
}

func (c *Checker) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

// markDown은 접속 실패한 서버를 이후 검증에서 제외합니다.
func (c *Checker) markDown(server Server, err error) {
	if c.down == nil {
		c.down = map[string]bool{}
	}
	if !c.down[server.Address] {
		c.down[server.Address] = true
		logx.Warn("DNS 서버 %s에 접속하지 못해 이후 검증에서 제외합니다: %v", server.Address, err)
	}
}

// Check는 모든 노드를 검증하고, 실패를 모아 한 번에 보고합니다.
// 문제를 하나 찾았다고 멈추면 DNS 등록을 한 번에 고칠 수 없으므로
// 전체를 확인한 뒤 실패 목록을 반환합니다.
func (c *Checker) Check(ctx context.Context, nodes []Node) error {
	if len(c.Servers) == 0 {
		return fmt.Errorf("DNS 검증에 사용할 서버가 없습니다. pathset의 network.dns를 확인하세요")
	}
	var problems []error
	for _, node := range nodes {
		fqdn, derived := FQDN(node.Hostname, c.Domain)
		if derived {
			logx.Info("%s: FQDN이 아니므로 임시 FQDN으로 검증합니다: %s", node.Hostname, fqdn)
		}
		if err := c.checkForward(ctx, fqdn, node.IP); err != nil {
			problems = append(problems, err)
		}
		if err := c.checkReverse(ctx, fqdn, node.IP); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("DNS 검증에 실패했습니다 (%d건):\n%w", len(problems), errors.Join(problems...))
	}
	return nil
}

// checkForward는 응답하는 모든 서버에서 FQDN의 정방향(A) 조회 결과에
// 기대 IP가 있는지 확인합니다. 서버 간 결과가 다르면(한쪽만 레코드가
// 있거나 IP가 다르면) 노드가 어느 서버를 쓰느냐에 따라 동작이 갈리므로
// 오류입니다.
func (c *Checker) checkForward(ctx context.Context, fqdn, ip string) error {
	want := net.ParseIP(ip)
	answered := false
	var problems []error
	var lastNetErr error
	for _, server := range c.Servers {
		if c.down[server.Address] {
			continue
		}
		addrs, err := c.queryA(ctx, server, fqdn)
		switch {
		case errors.Is(err, errNotFound):
			answered = true
			problems = append(problems, fmt.Errorf("정방향 실패: %s의 DNS 레코드가 없습니다 (서버 %s)", fqdn, server.Address))
		case abnormalResponse(err):
			// 서버가 살아서 오류 코드(SERVFAIL 등)나 해석 불가능한 응답을
			// 반환한 경우는 접속 실패가 아니라 검증 실패입니다. 노드가 이
			// 서버를 쓰면 이름 해석에 실패하므로 제외하고 넘어가면 안
			// 됩니다.
			answered = true
			problems = append(problems, fmt.Errorf("정방향 실패: %s 조회에 서버가 오류를 반환했습니다: %w", fqdn, err))
		case err != nil:
			c.markDown(server, err)
			lastNetErr = err
		default:
			answered = true
			if verr := verifyForward(server.Address, fqdn, want, addrs); verr != nil {
				problems = append(problems, verr)
			}
		}
	}
	if !answered {
		if lastNetErr != nil {
			return fmt.Errorf("정방향 실패: 응답한 DNS 서버가 없습니다: %s: %w", fqdn, lastNetErr)
		}
		return fmt.Errorf("정방향 실패: 응답한 DNS 서버가 없습니다: %s", fqdn)
	}
	return errors.Join(problems...)
}

// verifyForward는 조회된 주소 목록에 기대 IP가 있는지 확인합니다.
func verifyForward(server, fqdn string, want net.IP, addrs []net.IP) error {
	found := false
	extras := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Equal(want) {
			found = true
			continue
		}
		extras = append(extras, a.String())
	}
	if !found {
		return fmt.Errorf("정방향 불일치: %s -> %s (기대 IP: %s, 서버 %s)",
			fqdn, strings.Join(extras, ", "), want, server)
	}
	logx.Info("정방향 확인: %s -> %s (서버 %s)", fqdn, want, server)
	if len(extras) > 0 {
		// 노드 hostname이 여러 IP로 등록된 상태는 대개 등록 실수입니다.
		// 기대 IP가 포함되어 있으므로 오류로 처리하지 않고 경고를 남깁니다.
		logx.Warn("%s에 다른 IP도 등록되어 있습니다: %s (서버 %s)", fqdn, strings.Join(extras, ", "), server)
	}
	return nil
}

// checkReverse는 응답하는 모든 서버에서 IP의 역방향(PTR)을 확인합니다.
// 서버마다 PTR 레코드가 없으면 그 서버는 건너뛰고, 있으면 FQDN과
// 일치해야 합니다. 어느 한 서버라도 잘못된 PTR을 갖고 있으면 노드가
// 그 서버를 쓸 때 문제가 되므로 오류입니다. 첫 응답에서 멈추지 않고 모든
// 서버를 확인해야 다른 서버의 잘못된 PTR도 발견할 수 있습니다.
//
// 역방향 검증은 잘못된 PTR이 실제로 조회된 경우에만 실패합니다. PTR 레코드
// 없음, 비정상 응답, 접속 오류처럼 확인할 수 없는 경우에는 경고 후
// 건너뜁니다. 선택 항목인 역방향 조회의 접속 오류 때문에 필수인 정방향
// 검증에서 서버가 제외되지 않도록 down 상태도 변경하지 않습니다.
// 모든 서버가 정말 죽었다면 정방향 검증이 이미 실패를 보고합니다.
func (c *Checker) checkReverse(ctx context.Context, fqdn, ip string) error {
	checked := false
	var problems []error
	for _, server := range c.Servers {
		if c.down[server.Address] {
			continue
		}
		names, err := c.queryPTR(ctx, server, ip)
		switch {
		case errors.Is(err, errNotFound):
			checked = true
			logx.Info("역방향 건너뜀: %s의 PTR 레코드 없음 (서버 %s)", ip, server.Address)
		case abnormalResponse(err):
			// 역방향 존이 구성되지 않은 서버는 NXDOMAIN 대신 SERVFAIL을
			// 반환할 수 있으므로 PTR을 확인할 수 없는 경우로 처리합니다.
			checked = true
			logx.Warn("역방향 건너뜀: %s 조회에 서버가 오류를 반환했습니다(PTR 없음으로 간주): %v", ip, err)
		case err != nil:
			checked = true
			logx.Warn("역방향 건너뜀: %s 조회 중 서버 %s 접속 오류(PTR 없음으로 간주): %v", ip, server.Address, err)
		default:
			checked = true
			if verr := verifyReverse(server.Address, fqdn, ip, names); verr != nil {
				problems = append(problems, verr)
			}
		}
	}
	if !checked {
		logx.Warn("역방향 건너뜀: %s를 확인할 수 있는 DNS 서버가 없습니다", ip)
	}
	return errors.Join(problems...)
}

// verifyReverse는 PTR 이름 목록에 FQDN이 있는지 확인합니다.
// PTR 이름의 끝 점(.)은 제거하고 대소문자는 무시합니다.
func verifyReverse(server, fqdn, ip string, names []string) error {
	cleaned := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSuffix(name, ".")
		if strings.EqualFold(name, fqdn) {
			logx.Info("역방향 확인: %s -> %s (서버 %s)", ip, name, server)
			return nil
		}
		cleaned = append(cleaned, name)
	}
	return fmt.Errorf("역방향 불일치: %s -> %s (기대 FQDN: %s, 서버 %s)",
		ip, strings.Join(cleaned, ", "), fqdn, server)
}

// abnormalResponse는 서버가 응답은 했지만 정상 결과가 아닌 경우입니다
// (오류 코드 응답 또는 해석 불가능한 응답). 접속 실패(무응답)와 달리
// 서버는 살아 있으므로 down 표시로 제외하지 않고 검증 규칙을 적용합니다.
func abnormalResponse(err error) bool {
	var rcErr *rcodeError
	var pErr *protocolError
	return errors.As(err, &rcErr) || errors.As(err, &pErr)
}

func (c *Checker) queryA(ctx context.Context, server Server, fqdn string) ([]net.IP, error) {
	qctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	return server.Querier.QueryA(qctx, fqdn)
}

func (c *Checker) queryPTR(ctx context.Context, server Server, ip string) ([]string, error) {
	qctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	return server.Querier.QueryPTR(qctx, ip)
}
