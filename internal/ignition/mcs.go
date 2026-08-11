package ignition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// caSourcePrefix는 certificateAuthorities source data URL의 접두사입니다.
const caSourcePrefix = "data:text/plain;charset=utf-8;base64,"

// DialTimeout은 MCS 연결 확인에 사용하는 제한 시간입니다.
const DialTimeout = 5 * time.Second

// CheckMCS는 Machine Config Server에 TCP로 연결할 수 있는지 확인합니다.
func CheckMCS(ctx context.Context, endpoint string) error {
	dialer := &net.Dialer{Timeout: DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return fmt.Errorf("MCS에 연결할 수 없습니다: %s: %w", endpoint, err)
	}
	return conn.Close()
}

// FetchCASource는 MCS가 제시하는 인증서 체인을 읽어 Ignition의
// certificateAuthorities에 넣을 data URL을 만듭니다.
//
// crypto/tls로 직접 핸드셰이크한 뒤 서버가 보낸 인증서 체인을 PEM으로
// 인코딩합니다. 인증서를 로그나 임시 파일에 남기지 않습니다.
//
// 인증서 검증은 하지 않습니다(InsecureSkipVerify). 아직 클러스터 CA를
// 신뢰하지 않는 상태에서 그 CA를 가져오는 것이 이 함수의 목적이기 때문입니다.
// 가져온 체인은 호출 측에서 Ignition에 그대로 넣고, 이후 노드는 이 CA로 MCS를 검증합니다.
func FetchCASource(ctx context.Context, endpoint string) (string, error) {
	// tls.Dialer.DialContext를 써야 Ctrl+C(컨텍스트 취소)가 dial과
	// 핸드셰이크에 반영됩니다(DialWithDialer는 ctx를 받지 못합니다).
	tlsDialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: DialTimeout},
		Config: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // 클러스터 CA를 가져오기 위한 의도된 동작
			MinVersion:         tls.VersionTLS12,
		},
	}
	rawConn, err := tlsDialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return "", fmt.Errorf("MCS TLS 연결에 실패했습니다: %s: %w", endpoint, err)
	}
	defer rawConn.Close()
	conn, ok := rawConn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("MCS TLS 연결 형식이 예상과 다릅니다: %s", endpoint)
	}

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("MCS CA 인증서를 읽지 못했습니다: %s", endpoint)
	}

	var chain []byte
	for _, cert := range certs {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		})...)
	}
	if len(chain) == 0 {
		return "", fmt.Errorf("MCS CA 인증서를 PEM으로 변환하지 못했습니다: %s", endpoint)
	}

	encoded := base64.StdEncoding.EncodeToString(chain)
	return caSourcePrefix + encoded, nil
}

// CAFingerprints는 CA source 데이터 URL의 인증서 체인에서 CA 인증서의
// SHA-256 지문을 정렬해 반환합니다. 서버 인증서는 클러스터 재구축 없이도
// 회전할 수 있으므로 체인 전체가 아닌 신뢰 기준인 CA를 비교합니다. 체인에
// CA 인증서가 없으면 리프 인증서의 지문을 대신 사용합니다.
func CAFingerprints(caSource string) ([]string, error) {
	if !strings.HasPrefix(caSource, caSourcePrefix) {
		return nil, fmt.Errorf("CA source가 data URL 형식이 아닙니다")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(caSource, caSourcePrefix))
	if err != nil {
		return nil, fmt.Errorf("CA source base64를 해석하지 못했습니다: %w", err)
	}
	// CERTIFICATE 블록만 연속해서 허용합니다. pem.Decode는 잘못된 데이터를
	// 건너뛸 수 있으므로 각 블록을 데이터의 선두부터 엄격하게 해석합니다.
	var all, cas []string
	var caCerts, leafCerts []*x509.Certificate
	rest := raw
	for {
		trimmed := bytes.TrimSpace(rest)
		if len(trimmed) == 0 {
			break
		}
		der, next, err := leadingCertBlock(trimmed)
		if err != nil {
			return nil, fmt.Errorf("CA source: %w", err)
		}
		rest = next
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("CA source의 인증서를 해석하지 못했습니다: %w", err)
		}
		sum := sha256.Sum256(cert.Raw)
		fp := hex.EncodeToString(sum[:])
		all = append(all, fp)
		if cert.IsCA {
			cas = append(cas, fp)
			caCerts = append(caCerts, cert)
		} else {
			leafCerts = append(leafCerts, cert)
		}
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("CA source에 인증서가 없습니다")
	}
	// CA가 없는 경우에는 단일 리프 인증서만 허용합니다. CA 없이 여러 리프
	// 인증서가 들어 있는 묶음은 정상적인 인증서 체인으로 볼 수 없습니다.
	if len(caCerts) == 0 && len(leafCerts) > 1 {
		return nil, fmt.Errorf("CA source에 CA 없이 인증서 %d장이 있습니다 — 단일 serving 인증서이거나 CA를 포함한 체인이어야 합니다", len(leafCerts))
	}
	// CA와 리프 인증서가 함께 있으면 모든 리프가 포함된 CA로 검증되어야
	// 합니다. 리프만 있는 체인은 호환성을 위해 허용합니다. 인증서 만료는
	// 산출물의 출처 판단과 무관하므로 이 단계에서 검사하지 않습니다.
	if len(caCerts) > 0 && len(leafCerts) > 0 {
		pool := x509.NewCertPool()
		for _, cert := range caCerts {
			pool.AddCert(cert)
		}
		for _, leaf := range leafCerts {
			_, err := leaf.Verify(x509.VerifyOptions{
				Roots:         pool,
				Intermediates: pool,
				CurrentTime:   leaf.NotBefore.Add(time.Second),
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			})
			if err != nil {
				return nil, fmt.Errorf("CA source의 인증서들이 하나의 체인이 아닙니다 (leaf %q가 포함된 CA로 검증되지 않음): %w",
					leaf.Subject.CommonName, err)
			}
		}
	}
	out := cas
	if len(out) == 0 {
		out = all
	}
	sort.Strings(out)
	return out, nil
}

// leadingCertBlock은 데이터 선두의 CERTIFICATE PEM 블록 하나를 엄격하게
// 해석하고 DER과 나머지 데이터를 반환합니다.
//
// pem.Decode는 잘못된 데이터를 건너뛰고 뒤의 정상 블록을 반환할 수 있습니다.
// 이를 막기 위해 BEGIN 줄, base64 본문, END 줄이 순서대로 정확히 이어지는지
// 직접 확인합니다.
func leadingCertBlock(data []byte) (der []byte, rest []byte, err error) {
	beginMarker := []byte("-----BEGIN CERTIFICATE-----")
	endMarker := []byte("-----END CERTIFICATE-----")

	if !bytes.HasPrefix(data, beginMarker) {
		if bytes.HasPrefix(data, []byte("-----BEGIN ")) {
			line := data
			if i := bytes.IndexByte(line, '\n'); i >= 0 {
				line = line[:i]
			}
			return nil, nil, fmt.Errorf("인증서가 아닌 PEM 블록이 있습니다: %s", bytes.TrimSpace(line))
		}
		return nil, nil, fmt.Errorf("PEM이 아닌 데이터가 있습니다")
	}
	body := data[len(beginMarker):]
	endIdx := bytes.Index(body, endMarker)
	if endIdx < 0 {
		return nil, nil, fmt.Errorf("PEM 블록이 END 표시 없이 끝났습니다")
	}
	compact := bytes.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, body[:endIdx])
	der, err = base64.StdEncoding.DecodeString(string(compact))
	if err != nil {
		// 본문에 base64가 아닌 문자(임의 데이터, 다른 PEM 마커 등)가
		// 있다는 뜻입니다. 깨진 블록을 건너뛰지 않고 즉시 거부합니다.
		return nil, nil, fmt.Errorf("PEM 블록 본문을 해석하지 못했습니다: %w", err)
	}
	if len(der) == 0 {
		return nil, nil, fmt.Errorf("PEM 블록 본문이 비어 있습니다")
	}
	return der, body[endIdx+len(endMarker):], nil
}
