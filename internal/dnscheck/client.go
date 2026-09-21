package dnscheck

// 최소 DNS 와이어 프로토콜 클라이언트(A/PTR 전용)입니다.
//
// net.Resolver를 쓰지 않는 이유: Go 내장 리졸버는
// 사용자 지정 Dial을 주더라도 /etc/hosts와 resolv.conf의 search 도메인을
// 먼저 참조합니다. 그러면 bastion의 로컬 설정으로 검증이 통과할 수 있어
// "노드가 실제 사용할 DNS 서버의 등록 상태를 확인한다"는 목적이 깨집니다.
// 이 클라이언트는 지정한 서버에 지정한 이름 그대로만 질의합니다
// (hosts 파일 참조 없음, search 도메인 결합 없음).
//
// 외부 모듈 의존 0 정책에 따라 표준 라이브러리만 사용합니다(RFC 1035).

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// errNotFound는 "레코드가 없다"는 확정 응답입니다(NXDOMAIN 또는 빈 응답).
// 서버 접속 실패와 구분하기 위해 사용합니다.
var errNotFound = errors.New("DNS 레코드가 없습니다")

// rcodeError는 서버가 응답은 했지만 오류 코드(SERVFAIL, REFUSED 등)를
// 반환한 경우입니다. 접속 실패(서버 다운)와 달리 서버는 살아 있으므로,
// 노드가 이 서버를 쓰면 이름 해석에 실패합니다. 검증에서 접속 실패처럼
// 조용히 제외하면 안 됩니다.
type rcodeError struct {
	server string
	rcode  int
}

// protocolError는 서버가 응답을 보냈지만 올바른 DNS 메시지가 아닌
// 경우입니다(형식 오류, QR=0, 질의 ID 불일치 등). 접속 실패(무응답)와
// 달리 서버는 살아 있으므로, rcodeError와 같은 "비정상 응답"으로
// 분류해 조용히 제외하지 않습니다.
type protocolError struct {
	server string
	reason error
}

func (e *protocolError) Error() string {
	return fmt.Sprintf("DNS 서버 %s 응답이 올바르지 않습니다: %v", e.server, e.reason)
}

func (e *protocolError) Unwrap() error { return e.reason }

func (e *rcodeError) Error() string {
	name := ""
	switch e.rcode {
	case 1:
		name = "(FORMERR)"
	case 2:
		name = "(SERVFAIL)"
	case 4:
		name = "(NOTIMP)"
	case 5:
		name = "(REFUSED)"
	}
	return fmt.Sprintf("DNS 서버 %s 오류 응답 (RCODE %d%s)", e.server, e.rcode, name)
}

const (
	typeA    = 1
	typePTR  = 12
	classIN  = 1
	maxReads = 5 // ID가 다른(오래된/위조) UDP 응답을 건너뛰는 최대 횟수
)

// client는 DNS 서버 하나에 대한 Querier 구현입니다.
type client struct {
	server string // "IP:53"
}

// QueryA는 이름의 A 레코드 IP 목록을 반환합니다.
func (c *client) QueryA(ctx context.Context, fqdn string) ([]net.IP, error) {
	answers, err := c.exchange(ctx, fqdn, typeA)
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, a := range answers {
		if a.rtype == typeA && a.ip != nil {
			out = append(out, a.ip)
		}
	}
	if len(out) == 0 {
		// 이름은 있으나 A 레코드가 없는 경우(NODATA, CNAME만 있는 경우 포함).
		return nil, errNotFound
	}
	return out, nil
}

// QueryPTR은 IPv4 주소의 PTR 이름 목록을 반환합니다.
func (c *client) QueryPTR(ctx context.Context, ip string) ([]string, error) {
	arpa, err := reverseName(ip)
	if err != nil {
		return nil, err
	}
	answers, err := c.exchange(ctx, arpa, typePTR)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range answers {
		if a.rtype == typePTR && a.name != "" {
			out = append(out, a.name)
		}
	}
	if len(out) == 0 {
		return nil, errNotFound
	}
	return out, nil
}

// reverseName은 IPv4 주소의 역방향 조회 이름(in-addr.arpa)을 만듭니다.
func reverseName(ip string) (string, error) {
	v4 := net.ParseIP(ip).To4()
	if v4 == nil {
		return "", fmt.Errorf("역방향 조회는 IPv4 주소만 지원합니다: %q", ip)
	}
	return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", v4[3], v4[2], v4[1], v4[0]), nil
}

// answer는 응답의 리소스 레코드 하나입니다.
type answer struct {
	rtype uint16
	ip    net.IP // typeA
	name  string // typePTR
}

// exchange는 UDP로 질의하고, 응답이 잘렸으면(TC) TCP로 재시도합니다.
func (c *client) exchange(ctx context.Context, name string, qtype uint16) ([]answer, error) {
	query, id, err := buildQuery(name, qtype)
	if err != nil {
		return nil, err
	}
	resp, err := c.roundTripUDP(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("DNS 서버 %s: %w", c.server, err)
	}
	parsed, err := parseResponse(resp, id)
	if err != nil {
		// 응답은 받았으나 해석할 수 없음: 접속 실패가 아니라 비정상 응답.
		return nil, &protocolError{server: c.server, reason: err}
	}
	if parsed.truncated {
		resp, err = c.roundTripTCP(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("DNS 서버 %s: TCP 재시도 실패: %w", c.server, err)
		}
		parsed, err = parseResponse(resp, id)
		if err != nil {
			return nil, &protocolError{server: c.server, reason: err}
		}
	}
	switch parsed.rcode {
	case 0:
		return parsed.answers, nil
	case 3: // NXDOMAIN
		return nil, errNotFound
	default:
		return nil, &rcodeError{server: c.server, rcode: parsed.rcode}
	}
}

func deadlineFrom(ctx context.Context) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(defaultTimeout)
}

// roundTripUDP는 질의를 보내고 ID가 일치하는 응답을 기다립니다.
func (c *client) roundTripUDP(ctx context.Context, query []byte, id uint16) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", c.server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadlineFrom(ctx)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	for range maxReads {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= 2 && binary.BigEndian.Uint16(buf[:2]) == id {
			return append([]byte(nil), buf[:n]...), nil
		}
		// ID가 다른 응답은 이전 질의의 잔여물이거나 위조이므로 무시합니다.
	}
	// 응답 자체는 계속 오는데 ID가 전부 다름: 무응답이 아니라 비정상 응답.
	return nil, &protocolError{server: c.server,
		reason: errors.New("질의 ID와 일치하는 응답을 받지 못했습니다")}
}

// roundTripTCP는 2바이트 길이 접두사를 붙여 TCP로 질의합니다(RFC 1035 4.2.2).
func (c *client) roundTripTCP(ctx context.Context, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadlineFrom(ctx)); err != nil {
		return nil, err
	}
	msg := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(msg[:2], uint16(len(query)))
	copy(msg[2:], query)
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// buildQuery는 재귀 질의(RD=1) 메시지를 만듭니다.
func buildQuery(name string, qtype uint16) ([]byte, uint16, error) {
	qname, err := encodeName(name)
	if err != nil {
		return nil, 0, err
	}
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, 0, fmt.Errorf("질의 ID 생성 실패: %w", err)
	}
	id := binary.BigEndian.Uint16(idBytes[:])

	msg := make([]byte, 0, 12+len(qname)+4)
	msg = binary.BigEndian.AppendUint16(msg, id)
	msg = binary.BigEndian.AppendUint16(msg, 0x0100) // RD=1
	msg = binary.BigEndian.AppendUint16(msg, 1)      // QDCOUNT
	msg = append(msg, 0, 0, 0, 0, 0, 0)              // AN/NS/ARCOUNT
	msg = append(msg, qname...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	return msg, id, nil
}

// encodeName은 도메인 이름을 라벨 길이 접두사 형식으로 만듭니다.
func encodeName(name string) ([]byte, error) {
	if len(name) == 0 || len(name) > 253 {
		return nil, fmt.Errorf("도메인 이름 길이가 잘못됐습니다: %q", name)
	}
	out := make([]byte, 0, len(name)+2)
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			label := name[start:i]
			if len(label) == 0 || len(label) > 63 {
				return nil, fmt.Errorf("도메인 라벨 길이가 잘못됐습니다: %q", name)
			}
			out = append(out, byte(len(label)))
			out = append(out, label...)
			start = i + 1
		}
	}
	return append(out, 0), nil
}

// response는 파싱한 응답입니다.
type response struct {
	rcode     int
	truncated bool
	answers   []answer
}

// parseResponse는 응답 메시지에서 answer 섹션의 A/PTR 레코드를 꺼냅니다.
func parseResponse(msg []byte, wantID uint16) (*response, error) {
	if len(msg) < 12 {
		return nil, fmt.Errorf("DNS 응답이 너무 짧습니다 (%d바이트)", len(msg))
	}
	if binary.BigEndian.Uint16(msg[0:2]) != wantID {
		return nil, fmt.Errorf("DNS 응답 ID가 질의와 다릅니다")
	}
	flags := binary.BigEndian.Uint16(msg[2:4])
	if flags&0x8000 == 0 {
		return nil, fmt.Errorf("DNS 응답이 아닙니다(QR=0)")
	}
	out := &response{
		rcode:     int(flags & 0xF),
		truncated: flags&0x0200 != 0,
	}
	if out.truncated || out.rcode != 0 {
		return out, nil
	}

	qdCount := int(binary.BigEndian.Uint16(msg[4:6]))
	anCount := int(binary.BigEndian.Uint16(msg[6:8]))
	off := 12
	for range qdCount {
		var err error
		if _, off, err = decodeName(msg, off); err != nil {
			return nil, err
		}
		off += 4 // QTYPE + QCLASS
		if off > len(msg) {
			return nil, fmt.Errorf("DNS 응답 question 섹션이 잘렸습니다")
		}
	}
	for range anCount {
		var err error
		if _, off, err = decodeName(msg, off); err != nil {
			return nil, err
		}
		if off+10 > len(msg) {
			return nil, fmt.Errorf("DNS 응답 레코드 헤더가 잘렸습니다")
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		class := binary.BigEndian.Uint16(msg[off+2 : off+4])
		rdLen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdLen > len(msg) {
			return nil, fmt.Errorf("DNS 응답 레코드 데이터가 잘렸습니다")
		}
		switch {
		case rtype == typeA && class == classIN && rdLen == 4:
			ip := make(net.IP, 4)
			copy(ip, msg[off:off+4])
			out.answers = append(out.answers, answer{rtype: typeA, ip: ip})
		case rtype == typePTR && class == classIN:
			name, _, err := decodeName(msg, off)
			if err != nil {
				return nil, err
			}
			out.answers = append(out.answers, answer{rtype: typePTR, name: name})
		}
		off += rdLen
	}
	return out, nil
}

// decodeName은 off 위치의 이름을 읽습니다(압축 포인터 지원, RFC 1035 4.1.4).
// 반환하는 next는 원래 위치 기준으로 이름 다음 오프셋입니다.
func decodeName(msg []byte, off int) (name string, next int, err error) {
	var labels []byte
	next = -1 // 첫 포인터를 만나기 전까지는 미정
	jumps := 0
	for {
		if off >= len(msg) {
			return "", 0, fmt.Errorf("DNS 이름이 메시지 범위를 벗어났습니다")
		}
		b := int(msg[off])
		switch {
		case b == 0:
			if next < 0 {
				next = off + 1
			}
			return string(labels), next, nil
		case b&0xC0 == 0xC0: // 압축 포인터
			if off+1 >= len(msg) {
				return "", 0, fmt.Errorf("DNS 압축 포인터가 잘렸습니다")
			}
			if jumps++; jumps > 10 {
				return "", 0, fmt.Errorf("DNS 압축 포인터가 너무 깊습니다")
			}
			if next < 0 {
				next = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:off+2]) & 0x3FFF)
		case b&0xC0 != 0:
			return "", 0, fmt.Errorf("지원하지 않는 DNS 라벨 형식입니다: 0x%02x", b)
		default:
			if off+1+b > len(msg) {
				return "", 0, fmt.Errorf("DNS 라벨이 메시지 범위를 벗어났습니다")
			}
			if len(labels) > 0 {
				labels = append(labels, '.')
			}
			labels = append(labels, msg[off+1:off+1+b]...)
			if len(labels) > 255 {
				return "", 0, fmt.Errorf("DNS 이름이 너무 깁니다")
			}
			off += 1 + b
		}
	}
}
