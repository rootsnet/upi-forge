package cli

// iso의 기존 Ignition 출처 검증을 고정합니다.
// 같은 <hostname>.ign 이름이라도 다른 노드·다른 클러스터·재구축 전
// 클러스터의 산출물일 수 있으므로 hostname, MCS 주소, MCS 인증서를
// 현재 설정·클러스터와 대조합니다.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/logx"
)

// runISOProvenance는 iso를 실행하고 오류를 돌려줍니다.
// fakeCA가 비어 있지 않으면 MCS 인증서 조회를 그 값으로 대체하고,
// 비어 있으면 --skip-ca-check로 실행합니다.
func runISOProvenance(t *testing.T, configPath, fakeCA string) error {
	t.Helper()
	t.Setenv(envFakeInstaller, "1")

	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })

	args := []string{"--config", configPath, "iso"}
	if fakeCA == "" {
		args = append(args, "--skip-ca-check")
	} else {
		orig := fetchCASource
		fetchCASource = func(context.Context, string) (string, error) { return fakeCA, nil }
		t.Cleanup(func() { fetchCASource = orig })
	}
	return Execute(context.Background(), args)
}

func writeNodeIgnition(t *testing.T, configPath, content string) {
	t.Helper()
	// isoGuidanceEnv의 workspace 위치는 설정 파일에 적혀 있으므로
	// 기존 .ign을 찾아 내용만 바꿉니다.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var workspace string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "dir: '") {
			workspace = strings.TrimSuffix(strings.SplitN(line, "'", 3)[1], "'")
			break
		}
	}
	if workspace == "" {
		t.Fatal("설정에서 workspace.dir을 찾지 못했습니다")
	}
	if err := os.WriteFile(filepath.Join(workspace, isoTestNode+".ign"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 다른 노드의 Ignition(파일 이름만 같음)은 거부해야 합니다.
func TestISORejectsIgnitionForOtherHostname(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath, testIgnition("other.mycluster.example.com", testMCSSource, testCAURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "hostname이 대상 노드와 다릅니다") {
		t.Fatalf("다른 노드의 Ignition을 거부해야 합니다: %v", err)
	}
}

// 다른 클러스터(MCS 주소가 다름)의 Ignition은 거부해야 합니다.
func TestISORejectsIgnitionForOtherMCS(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, "https://api-int.other.example.com:22623/config/worker", testCAURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "MCS 주소가 현재 설정과 다릅니다") {
		t.Fatalf("다른 클러스터의 Ignition을 거부해야 합니다: %v", err)
	}
}

// 같은 도메인으로 재구축된 클러스터: 이름과 MCS 주소는 같지만 CA가
// 다릅니다. 라이브 대조가 이를 잡아야 합니다.
func TestISORejectsStaleCAAfterClusterRebuild(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)

	err := runISOProvenance(t, configPath, testRebuiltCAURL(t))
	if err == nil || !strings.Contains(err.Error(), "재구축") {
		t.Fatalf("재구축된 클러스터의 오래된 CA를 잡아야 합니다: %v", err)
	}
}

// 회귀 테스트: serving 인증서만 회전되고 CA가
// 같으면 같은 클러스터입니다. 체인 문자열 전체를 비교하면 이 정상
// 상황이 "재구축"으로 오판되므로, CA 지문 비교가 이를 통과시켜야 합니다.
func TestISOAcceptsRotatedServingCertSameCA(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)

	// 산출물은 serving1+CA, 현재 클러스터는 serving2+CA (같은 CA).
	if err := runISOProvenance(t, configPath, testRotatedURL(t)); err != nil {
		t.Fatalf("CA가 같으면 serving 회전은 통과해야 합니다: %v", err)
	}
}

// 인증서 체인이 완전히 같으면 당연히 통과합니다.
func TestISOAcceptsMatchingCA(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)

	if err := runISOProvenance(t, configPath, testCAURL(t)); err != nil {
		t.Fatalf("인증서가 일치하면 통과해야 합니다: %v", err)
	}
}

// 회귀 테스트: merge 항목을 하나 더 끼워 넣어
// 첫 항목만 검사하는 허점을 노리는 산출물은 --skip-ca-check로도
// 통과하면 안 됩니다(이 도구는 항상 정확히 하나만 만듭니다).
func TestISORejectsExtraMergeEntry(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	ign := `{"ignition":{"version":"3.5.0",` +
		`"config":{"merge":[{"source":"` + testMCSSource + `"},` +
		`{"source":"https://api-int.other.example.com:22623/config/worker"}]},` +
		`"security":{"tls":{"certificateAuthorities":[{"source":"` + testCAURL(t) + `"}]}}},` +
		`"storage":{"files":[{"path":"/etc/hostname",` +
		`"contents":{"source":"data:text/plain;charset=utf-8,` + isoTestNode + `"},"mode":420}]}}`
	writeNodeIgnition(t, configPath, ign)

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "merge 항목은 정확히 하나여야") {
		t.Fatalf("merge 항목이 여러 개면 거부해야 합니다: %v", err)
	}
}

// CA 항목을 하나 더 끼워 넣은 산출물도 마찬가지입니다.
func TestISORejectsExtraCAEntry(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	ign := `{"ignition":{"version":"3.5.0",` +
		`"config":{"merge":[{"source":"` + testMCSSource + `"}]},` +
		`"security":{"tls":{"certificateAuthorities":[{"source":"` + testCAURL(t) + `"},` +
		`{"source":"` + testRebuiltCAURL(t) + `"}]}}},` +
		`"storage":{"files":[{"path":"/etc/hostname",` +
		`"contents":{"source":"data:text/plain;charset=utf-8,` + isoTestNode + `"},"mode":420}]}}`
	writeNodeIgnition(t, configPath, ign)

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "certificateAuthorities 항목은 정확히 하나여야") {
		t.Fatalf("CA 항목이 여러 개면 거부해야 합니다: %v", err)
	}
}

// 회귀 테스트: 서로 무관한 인증서를 묶은 PEM
// (leaf를 서명하지 않은 CA와 동봉)은 실제 체인이 아니므로
// --skip-ca-check로도 구조 검사에서 거부해야 합니다.
func TestISORejectsIncoherentCAChain(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, testMismatchedURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "하나의 체인이 아닙니다") {
		t.Fatalf("체인이 아닌 인증서 묶음은 거부해야 합니다: %v", err)
	}
}

// CA 없이 leaf만 제시하는 MCS 환경의 산출물은 체인 검증 대상이 없으므로
// 구조 검사를 통과해야 합니다(호환성 고정 — 엄격화로 되돌리면 실패).
func TestISOAcceptsLeafOnlyCAChain(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, testLeafOnlyURL(t)))

	if err := runISOProvenance(t, configPath, ""); err != nil {
		t.Fatalf("leaf만 있는 체인은 통과해야 합니다: %v", err)
	}
}

// 회귀 테스트: /etc/hostname 항목이 중복이면
// 첫 항목만 검사한 결과가 실제 적용값과 다를 수 있으므로 거부해야 합니다.
func TestISORejectsDuplicateHostnameEntries(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	ign := `{"ignition":{"version":"3.5.0",` +
		`"config":{"merge":[{"source":"` + testMCSSource + `"}]},` +
		`"security":{"tls":{"certificateAuthorities":[{"source":"` + testCAURL(t) + `"}]}}},` +
		`"storage":{"files":[` +
		`{"path":"/etc/hostname","contents":{"source":"data:text/plain;charset=utf-8,` + isoTestNode + `"},"mode":420},` +
		`{"path":"/etc/hostname","contents":{"source":"data:text/plain;charset=utf-8,evil.mycluster.example.com"},"mode":420}]}}`
	writeNodeIgnition(t, configPath, ign)

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "중복 path") {
		t.Fatalf("중복 /etc/hostname은 거부해야 합니다: %v", err)
	}
}

// 회귀 테스트: 인증서 사이에 다른 PEM 블록
// (개인 키 등)이나 PEM 아닌 데이터가 끼어 있으면 --skip-ca-check로도
// 거부해야 합니다.
func TestISORejectsNonCertPEMBlock(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, testNonCertPEMURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "인증서가 아닌 PEM 블록") {
		t.Fatalf("인증서 아닌 PEM 블록은 거부해야 합니다: %v", err)
	}
}

func TestISORejectsGarbageMixedWithCert(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, testGarbageMixURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "PEM이 아닌 데이터") {
		t.Fatalf("PEM 아닌 데이터가 섞이면 거부해야 합니다: %v", err)
	}
}

// 회귀 테스트: pem.Decode는 깨진 블록을
// 건너뛰고 뒤의 정상 인증서를 찾아 주므로, 이에 의존하면 "깨진 블록 +
// 정상 인증서" 묶음이 통과합니다. 선두 블록을 직접 해석하는 파서는
// 깨진 블록에서 즉시 거부해야 합니다.
func TestISORejectsBrokenBlockBeforeValidCert(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, testBrokenFirstURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "본문을 해석하지 못했습니다") {
		t.Fatalf("깨진 블록 뒤의 정상 인증서로 우회하면 안 됩니다: %v", err)
	}
}

// 회귀 테스트: CA 없는 예외는 단일 serving
// 인증서만입니다. CA도 없이 leaf 여러 장인 묶음은 정상 체인 형태가
// 아니므로 거부해야 합니다.
func TestISORejectsMultipleLeavesWithoutCA(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, testTwoLeavesURL(t)))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "CA 없이 인증서") {
		t.Fatalf("CA 없는 복수 leaf는 거부해야 합니다: %v", err)
	}
}

// CA 값이 인증서가 아니면 --skip-ca-check(대조 생략)와 무관하게
// 구조 검사에서 거부해야 합니다.
func TestISORejectsGarbageCAEvenWithSkip(t *testing.T) {
	configPath := isoGuidanceEnv(t, false, false)
	writeNodeIgnition(t, configPath,
		testIgnition(isoTestNode, testMCSSource, "data:text/plain;charset=utf-8;base64,bm90LWEtY2VydA=="))

	err := runISOProvenance(t, configPath, "")
	if err == nil || !strings.Contains(err.Error(), "CA 항목이 올바르지 않습니다") {
		t.Fatalf("인증서가 아닌 CA 값은 거부해야 합니다: %v", err)
	}
}
