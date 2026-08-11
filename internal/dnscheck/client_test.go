package dnscheck

// 와이어 클라이언트를 실제 UDP/TCP 가짜 DNS 서버로 검증합니다.
// 특히 /etc/hosts에 있는 이름(localhost)도 지정한 서버에 질의하는지
// 확인해, "지정 DNS가 아니라 hosts 결과로 통과"하는 회귀를 막습니다.

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testRR은 가짜 서버가 돌려줄 리소스 레코드 하나입니다.
type testRR struct {
	rtype  uint16
	ip     net.IP // typeA
	target string // typePTR
}

// buildResponse는 질의를 그대로 되돌려주는 형식의 응답을 만듭니다.
func buildResponse(query []byte, rcode int, tc bool, rrs ...testRR) []byte {
	msg := make([]byte, 0, 512)
	msg = append(msg, query[0], query[1]) // ID 복사
	flags := uint16(0x8180) | uint16(rcode&0xF)
	if tc {
		flags |= 0x0200
	}
	msg = binary.BigEndian.AppendUint16(msg, flags)
	msg = binary.BigEndian.AppendUint16(msg, 1)                // QDCOUNT
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(rrs))) // ANCOUNT
	msg = append(msg, 0, 0, 0, 0)                              // NS/ARCOUNT
	msg = append(msg, query[12:]...)                           // question 복사
	for _, rr := range rrs {
		msg = append(msg, 0xC0, 0x0C) // 이름: question으로의 압축 포인터
		msg = binary.BigEndian.AppendUint16(msg, rr.rtype)
		msg = binary.BigEndian.AppendUint16(msg, classIN)
		msg = append(msg, 0, 0, 0, 60) // TTL
		var rdata []byte
		if rr.rtype == typeA {
			rdata = rr.ip.To4()
		} else {
			rdata, _ = encodeName(rr.target)
		}
		msg = binary.BigEndian.AppendUint16(msg, uint16(len(rdata)))
		msg = append(msg, rdata...)
	}
	return msg
}

// queryName은 질의 메시지에서 이름과 타입을 꺼냅니다.
// 가짜 서버 고루틴에서 호출되므로 Fatalf 대신 Errorf를 씁니다
// (FailNow는 테스트 고루틴에서만 호출할 수 있습니다).
func queryName(t *testing.T, query []byte) (string, uint16) {
	t.Helper()
	name, off, err := decodeName(query, 12)
	if err != nil || off+2 > len(query) {
		t.Errorf("질의 이름 파싱 실패: %v", err)
		return "", 0
	}
	return name, binary.BigEndian.Uint16(query[off : off+2])
}

// startFakeDNS는 UDP 가짜 DNS 서버를 띄우고 주소를 반환합니다.
func startFakeDNS(t *testing.T, handler func(query []byte) []byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("UDP 리슨 실패: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := handler(append([]byte(nil), buf[:n]...)); resp != nil {
				pc.WriteTo(resp, addr) //nolint:errcheck
			}
		}
	}()
	return pc.LocalAddr().String()
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClientQueryA(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		name, qtype := queryName(t, query)
		if name != "worker1.example.test" || qtype != typeA {
			t.Errorf("예상 밖의 질의: %s (type %d)", name, qtype)
		}
		return buildResponse(query, 0, false, testRR{rtype: typeA, ip: net.ParseIP("192.0.2.10")})
	})
	ips, err := (&client{server: addr}).QueryA(testCtx(t), "worker1.example.test")
	if err != nil {
		t.Fatalf("QueryA 실패: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.10")) {
		t.Fatalf("A 레코드가 다릅니다: %v", ips)
	}
}

// /etc/hosts에 있는 이름도 반드시 지정한 서버에 질의해야 합니다.
// net.Resolver는 localhost를 hosts 파일에서 찾아 서버에 묻지 않으므로,
// 이 테스트는 hosts 우회 회귀를 잡습니다.
func TestClientQueryABypassesHostsFile(t *testing.T) {
	// 핸들러는 서버 고루틴에서 실행되므로 원자적으로 기록합니다.
	var asked atomic.Bool
	addr := startFakeDNS(t, func(query []byte) []byte {
		name, _ := queryName(t, query)
		if name == "localhost" {
			asked.Store(true)
		}
		return buildResponse(query, 0, false, testRR{rtype: typeA, ip: net.ParseIP("192.0.2.99")})
	})
	ips, err := (&client{server: addr}).QueryA(testCtx(t), "localhost")
	if err != nil {
		t.Fatalf("QueryA 실패: %v", err)
	}
	if !asked.Load() {
		t.Fatal("localhost 질의가 지정 서버에 도달하지 않았습니다 (hosts 파일 우회 실패)")
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.99")) {
		t.Fatalf("서버 응답이 아니라 다른 경로의 결과입니다: %v", ips)
	}
}

func TestClientQueryANXDomain(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		return buildResponse(query, 3, false) // NXDOMAIN
	})
	_, err := (&client{server: addr}).QueryA(testCtx(t), "missing.example.test")
	if !errors.Is(err, errNotFound) {
		t.Fatalf("NXDOMAIN은 errNotFound여야 합니다: %v", err)
	}
}

func TestClientQueryANoData(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		return buildResponse(query, 0, false) // 응답은 정상이지만 레코드 없음
	})
	_, err := (&client{server: addr}).QueryA(testCtx(t), "nodata.example.test")
	if !errors.Is(err, errNotFound) {
		t.Fatalf("NODATA는 errNotFound여야 합니다: %v", err)
	}
}

func TestClientQueryAServFail(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		return buildResponse(query, 2, false) // SERVFAIL
	})
	_, err := (&client{server: addr}).QueryA(testCtx(t), "worker1.example.test")
	if errors.Is(err, errNotFound) {
		t.Fatalf("SERVFAIL은 레코드 없음이 아닙니다: %v", err)
	}
	// 접속 실패와 구분되는 타입이어야 검증 단계에서 오류로 다뤄집니다.
	var rcErr *rcodeError
	if !errors.As(err, &rcErr) || rcErr.rcode != 2 {
		t.Fatalf("SERVFAIL은 rcodeError(RCODE 2)여야 합니다: %v", err)
	}
}

// 해석할 수 없는 응답은 접속 실패가 아니라 protocolError여야 합니다.
// 검증 단계가 이 타입으로 "서버가 살아 있는데 비정상"을 구분합니다.
func TestClientMalformedResponseIsProtocolError(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		// ID는 맞지만 헤더조차 안 되는 잘린 응답.
		return []byte{query[0], query[1], 0xDE, 0xAD}
	})
	_, err := (&client{server: addr}).QueryA(testCtx(t), "worker1.example.test")
	var pErr *protocolError
	if !errors.As(err, &pErr) {
		t.Fatalf("깨진 응답은 protocolError여야 합니다: %v", err)
	}
}

// 질의가 아닌 응답(QR=0)도 protocolError입니다.
func TestClientNonResponseIsProtocolError(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		resp := buildResponse(query, 0, false)
		resp[2] &^= 0x80 // QR 비트 제거
		return resp
	})
	_, err := (&client{server: addr}).QueryA(testCtx(t), "worker1.example.test")
	var pErr *protocolError
	if !errors.As(err, &pErr) {
		t.Fatalf("QR=0 응답은 protocolError여야 합니다: %v", err)
	}
}

func TestClientQueryPTR(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		name, qtype := queryName(t, query)
		if name != "5.2.0.192.in-addr.arpa" || qtype != typePTR {
			t.Errorf("역방향 질의 이름이 다릅니다: %s (type %d)", name, qtype)
		}
		return buildResponse(query, 0, false, testRR{rtype: typePTR, target: "worker1.example.test"})
	})
	names, err := (&client{server: addr}).QueryPTR(testCtx(t), "192.0.2.5")
	if err != nil {
		t.Fatalf("QueryPTR 실패: %v", err)
	}
	if len(names) != 1 || names[0] != "worker1.example.test" {
		t.Fatalf("PTR 이름이 다릅니다: %v", names)
	}
}

// UDP 응답이 잘렸으면(TC=1) 같은 주소의 TCP로 재시도해야 합니다.
func TestClientTruncatedFallsBackToTCP(t *testing.T) {
	addr := startFakeDNS(t, func(query []byte) []byte {
		return buildResponse(query, 0, true) // TC=1
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("같은 포트의 TCP 리슨 실패: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var lenBuf [2]byte
		if _, err := readFull(conn, lenBuf[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
		if _, err := readFull(conn, query); err != nil {
			return
		}
		resp := buildResponse(query, 0, false, testRR{rtype: typeA, ip: net.ParseIP("192.0.2.20")})
		out := make([]byte, 2+len(resp))
		binary.BigEndian.PutUint16(out[:2], uint16(len(resp)))
		copy(out[2:], resp)
		conn.Write(out) //nolint:errcheck
	}()

	ips, err := (&client{server: addr}).QueryA(testCtx(t), "big.example.test")
	if err != nil {
		t.Fatalf("TCP 재시도가 실패했습니다: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.20")) {
		t.Fatalf("TCP 응답이 다릅니다: %v", ips)
	}
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestReverseName(t *testing.T) {
	got, err := reverseName("192.0.2.20")
	if err != nil || got != "20.2.0.192.in-addr.arpa" {
		t.Fatalf("reverseName = %q, %v", got, err)
	}
	if _, err := reverseName("fd00::1"); err == nil {
		t.Fatal("IPv6는 오류여야 합니다")
	}
	if _, err := reverseName("not-an-ip"); err == nil {
		t.Fatal("IP가 아니면 오류여야 합니다")
	}
}

func TestEncodeNameRejectsBadLabels(t *testing.T) {
	if _, err := encodeName(""); err == nil {
		t.Error("빈 이름은 오류여야 합니다")
	}
	if _, err := encodeName("a..b"); err == nil {
		t.Error("빈 라벨은 오류여야 합니다")
	}
	if _, err := encodeName(strings.Repeat("x", 64) + ".test"); err == nil {
		t.Error("64자 라벨은 오류여야 합니다")
	}
}

// decodeName이 압축 포인터를 따라가는지 직접 확인합니다.
func TestDecodeNameCompression(t *testing.T) {
	msg := make([]byte, 12)
	// 오프셋 12: example.test
	msg = append(msg, 7)
	msg = append(msg, "example"...)
	msg = append(msg, 4)
	msg = append(msg, "test"...)
	msg = append(msg, 0)
	// 오프셋 26: worker1 + (오프셋 12로의 포인터)
	ptrOff := len(msg)
	msg = append(msg, 7)
	msg = append(msg, "worker1"...)
	msg = append(msg, 0xC0, 12)

	name, next, err := decodeName(msg, ptrOff)
	if err != nil {
		t.Fatalf("decodeName 실패: %v", err)
	}
	if name != "worker1.example.test" {
		t.Errorf("이름이 다릅니다: %q", name)
	}
	if next != len(msg) {
		t.Errorf("다음 오프셋이 다릅니다: %d != %d", next, len(msg))
	}
}

// 포인터가 자기 자신을 가리키면 무한 루프 대신 오류가 나야 합니다.
func TestDecodeNamePointerLoopFails(t *testing.T) {
	msg := make([]byte, 12)
	loopOff := len(msg)
	msg = append(msg, 0xC0, byte(loopOff))
	if _, _, err := decodeName(msg, loopOff); err == nil {
		t.Fatal("포인터 루프는 오류여야 합니다")
	}
}
