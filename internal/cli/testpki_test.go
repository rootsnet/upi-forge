package cli

// 출처 검증 테스트용 인증서 체계입니다. CA 지문 비교를 검증하려면 진짜
// x509 인증서가 필요하므로, 같은 CA로 서명한 serving 인증서 두 개(회전
// 시나리오)와 다른 CA(재구축 시나리오)를 한 번만 만들어 공유합니다.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"
)

var testPKI struct {
	once sync.Once
	err  error

	clusterCAURL   string // serving1 + CA — ignition 생성 시점의 체인
	rotatedLeafURL string // serving2 + CA — serving만 회전된 현재 체인
	rebuiltCAURL   string // serving + 다른 CA — 재구축된 클러스터의 체인
	mismatchedURL  string // serving1 + 무관한 CA — 체인이 아닌 인증서 묶음
	leafOnlyURL    string // serving1만 — CA를 제시하지 않는 MCS 환경
	nonCertPEMURL  string // serving1 + PRIVATE KEY 블록 — 인증서 아닌 PEM 포함
	garbageMixURL  string // serving1 + PEM 아닌 데이터 — 임의 데이터 포함
	twoLeavesURL   string // serving1 + serving2 (CA 없음) — 비정상 묶음
	brokenFirstURL string // 깨진 인증서 블록 + 정상 serving1 — 건너뛰기 우회 시도
}

func testCAURL(t *testing.T) string          { pkiInit(t); return testPKI.clusterCAURL }
func testRotatedURL(t *testing.T) string     { pkiInit(t); return testPKI.rotatedLeafURL }
func testRebuiltCAURL(t *testing.T) string   { pkiInit(t); return testPKI.rebuiltCAURL }
func testMismatchedURL(t *testing.T) string  { pkiInit(t); return testPKI.mismatchedURL }
func testLeafOnlyURL(t *testing.T) string    { pkiInit(t); return testPKI.leafOnlyURL }
func testNonCertPEMURL(t *testing.T) string  { pkiInit(t); return testPKI.nonCertPEMURL }
func testGarbageMixURL(t *testing.T) string  { pkiInit(t); return testPKI.garbageMixURL }
func testTwoLeavesURL(t *testing.T) string   { pkiInit(t); return testPKI.twoLeavesURL }
func testBrokenFirstURL(t *testing.T) string { pkiInit(t); return testPKI.brokenFirstURL }

func pkiInit(t *testing.T) {
	t.Helper()
	testPKI.once.Do(func() { testPKI.err = buildTestPKI() })
	if testPKI.err != nil {
		t.Fatalf("테스트 인증서 생성 실패: %v", testPKI.err)
	}
}

func buildTestPKI() error {
	ca1PEM, ca1Cert, ca1Key, err := makeTestCA("upi-forge-test-ca-1")
	if err != nil {
		return err
	}
	ca2PEM, ca2Cert, ca2Key, err := makeTestCA("upi-forge-test-ca-2")
	if err != nil {
		return err
	}
	leaf1, err := makeTestLeaf("mcs-serving-1", ca1Cert, ca1Key)
	if err != nil {
		return err
	}
	leaf2, err := makeTestLeaf("mcs-serving-2", ca1Cert, ca1Key)
	if err != nil {
		return err
	}
	leafX, err := makeTestLeaf("mcs-serving-x", ca2Cert, ca2Key)
	if err != nil {
		return err
	}
	testPKI.clusterCAURL = caDataURL(leaf1, ca1PEM)
	testPKI.rotatedLeafURL = caDataURL(leaf2, ca1PEM)
	testPKI.rebuiltCAURL = caDataURL(leafX, ca2PEM)
	testPKI.mismatchedURL = caDataURL(leaf1, ca2PEM) // leaf1은 ca2가 서명하지 않음
	testPKI.leafOnlyURL = caDataURL(leaf1)
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("dummy")})
	testPKI.nonCertPEMURL = caDataURL(leaf1, keyBlock)
	testPKI.garbageMixURL = caDataURL(leaf1, []byte("this-is-not-pem\n"))
	testPKI.twoLeavesURL = caDataURL(leaf1, leaf2)
	broken := []byte("-----BEGIN CERTIFICATE-----\nnot!!valid###base64\n-----END CERTIFICATE-----\n")
	testPKI.brokenFirstURL = caDataURL(broken, leaf1)
	return nil
}

func makeTestCA(name string) (pemBytes []byte, cert *x509.Certificate, key ed25519.PrivateKey, err error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key, nil
}

func makeTestLeaf(name string, ca *x509.Certificate, caKey ed25519.PrivateKey) ([]byte, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// caDataURL은 PEM 조각들을 FetchCASource와 같은 data URL로 만듭니다.
func caDataURL(pems ...[]byte) string {
	var chain []byte
	for _, p := range pems {
		chain = append(chain, p...)
	}
	return fmt.Sprintf("data:text/plain;charset=utf-8;base64,%s",
		base64.StdEncoding.EncodeToString(chain))
}
