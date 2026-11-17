package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/mirror"
)

// setupExtract는 테스트용 pull secret, 로그 경로, oc 대역 환경과 Options를 준비합니다.
func setupExtract(t *testing.T, idms, mos, fail string) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	pull := filepath.Join(dir, "pull.json")
	if err := os.WriteFile(pull, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "oc.log")
	t.Setenv(envFakeOC, "1")
	t.Setenv(envOCLog, logf)
	t.Setenv(envOCMOS, mos)
	t.Setenv(envOCIDMS, idms)
	t.Setenv(envOCFail, fail)

	sets, err := mirror.Load(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("mirror.Load: %v", err)
	}
	return Options{
		OCPath:       os.Args[0],
		Version:      "4.22.5",
		Registry:     "mirror.local:8443/openshift",
		BaseRegistry: "mirror.local:8443",
		PullSecret:   pull,
		TmpDir:       filepath.Join(dir, "tmp"),
		ISOTarget:    filepath.Join(dir, "full.iso"),
		InstallerTgt: filepath.Join(dir, "coreos-installer"),
		// release 이미지에 미러 주소를 지정해 release info의 후보를 하나로 제한합니다.
		ReleaseImage: "mirror.local:8443/openshift/release-images:4.22.5-x86_64",
		Sets:         sets,
	}, logf
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// 첫 번째 미러에서 추출에 실패하면 두 번째 미러를 시도해 성공하는지 확인합니다.
func TestExtractRetriesToSecondMirror(t *testing.T) {
	idms := `{"items":[{"spec":{"imageDigestMirrors":[
      {"source":"quay.io/openshift-release-dev/ocp-v4.0-art-dev",
       "mirrors":["mirror-a.local/release","mirror-b.local/release"]}
    ]}}]}`
	mos := "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaa"
	o, logf := setupExtract(t, idms, mos, "mirror-a.local")

	if err := Extract(context.Background(), o); err != nil {
		t.Fatalf("Extract 실패: %v", err)
	}
	log := readLog(t, logf)
	if !strings.Contains(log, "EXTRACT mirror-a.local/release@sha256:aaa") {
		t.Errorf("첫 미러를 시도해야 합니다:\n%s", log)
	}
	if !strings.Contains(log, "EXTRACT mirror-b.local/release@sha256:aaa") {
		t.Errorf("두 번째 미러로 재시도해 성공해야 합니다:\n%s", log)
	}
	if strings.Contains(log, "EXTRACT quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaa") {
		t.Errorf("성공했으면 원본 레지스트리를 시도하면 안 됩니다:\n%s", log)
	}
	// 최종 경로에 ISO 파일이 생성되었는지 확인합니다.
	if _, err := os.Stat(o.ISOTarget); err != nil {
		t.Errorf("ISO가 준비되어야 합니다: %v", err)
	}
}

// NeverContactSource 정책에서는 미러가 모두 실패해도 원본을 시도하지 않고
// 오류로 중단하는지 확인합니다.
func TestExtractNeverContactSource(t *testing.T) {
	idms := `{"items":[{"spec":{"imageDigestMirrors":[
      {"source":"quay.io/openshift-release-dev/ocp-v4.0-art-dev",
       "mirrors":["mirror-a.local/release"],
       "mirrorSourcePolicy":"NeverContactSource"}
    ]}}]}`
	mos := "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:bbb"
	o, logf := setupExtract(t, idms, mos, "mirror-a.local")

	err := Extract(context.Background(), o)
	if err == nil {
		t.Fatal("미러가 모두 실패하면 오류여야 합니다")
	}
	log := readLog(t, logf)
	if !strings.Contains(log, "EXTRACT mirror-a.local/release@sha256:bbb") {
		t.Errorf("미러는 시도해야 합니다:\n%s", log)
	}
	if strings.Contains(log, "EXTRACT quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:bbb") {
		t.Errorf("NeverContactSource이면 원본을 시도하면 안 됩니다:\n%s", log)
	}
}

// 권한 오류로 미러 설정을 읽지 못하면 고정 경로 치환을 적용해
// 설정된 미러 저장소에서 이미지를 추출하는지 확인합니다.
func TestExtractFallsBackWhenMirrorConfigUnreadable(t *testing.T) {
	dir := t.TempDir()
	pull := filepath.Join(dir, "pull.json")
	if err := os.WriteFile(pull, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "oc.log")
	t.Setenv(envFakeOC, "1")
	t.Setenv(envOCLog, logf)
	t.Setenv(envOCGetForbid, "1") // IDMS/ITMS/ICSP 모두 Forbidden
	t.Setenv(envOCMOS, "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaa")
	t.Setenv(envOCFail, "quay.io") // 혹시 원본을 시도하면 실패하도록

	sets, err := mirror.Load(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("mirror.Load: %v", err)
	}
	if sets.Reliable() {
		t.Fatal("모든 조회 실패 시 Reliable()은 false여야 합니다")
	}
	o := Options{
		OCPath: os.Args[0], Version: "4.22.5",
		Registry: "mirror.local:8443", BaseRegistry: "mirror.local:8443",
		PullSecret: pull, TmpDir: filepath.Join(dir, "tmp"),
		ISOTarget: filepath.Join(dir, "full.iso"), InstallerTgt: filepath.Join(dir, "coreos-installer"),
		ReleaseImage: "mirror.local:8443/openshift/release-images:4.22.5-x86_64",
		Sets:         sets,
	}
	if err := Extract(context.Background(), o); err != nil {
		t.Fatalf("폴백 추출이 성공해야 합니다: %v", err)
	}
	log := readLog(t, logf)
	if !strings.Contains(log, "EXTRACT mirror.local:8443/openshift/release@sha256:aaa") {
		t.Errorf("기존 폴백(미러 경로)으로 추출해야 합니다:\n%s", log)
	}
	if strings.Contains(log, "EXTRACT quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaa") {
		t.Errorf("인터넷 원본에서 추출하면 안 됩니다:\n%s", log)
	}
}

// release info가 첫 번째 미러에서 실패하면 두 번째 미러를 시도하는지 확인합니다.
func TestReleaseInfoRetriesCandidates(t *testing.T) {
	dir := t.TempDir()
	pull := filepath.Join(dir, "pull.json")
	if err := os.WriteFile(pull, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "oc.log")
	// release 이미지의 source에 두 미러를 지정합니다.
	idms := `{"items":[{"spec":{"imageDigestMirrors":[
      {"source":"quay.io/openshift-release-dev/ocp-release",
       "mirrors":["rel-a.local/release","rel-b.local/release"]}
    ]}}]}`
	t.Setenv(envFakeOC, "1")
	t.Setenv(envOCLog, logf)
	t.Setenv(envOCIDMS, idms)
	t.Setenv(envOCMOS, "mirror.local:8443/openshift/release@sha256:ccc") // 미러 주소 → 추출 성공
	t.Setenv(envOCFailInfo, "rel-a.local")                               // 첫 후보 release info 실패

	sets, err := mirror.Load(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("mirror.Load: %v", err)
	}
	o := Options{
		OCPath: os.Args[0], Version: "4.22.5",
		Registry: "mirror.local:8443", BaseRegistry: "mirror.local:8443",
		PullSecret: pull, TmpDir: filepath.Join(dir, "tmp"),
		ISOTarget: filepath.Join(dir, "full.iso"), InstallerTgt: filepath.Join(dir, "coreos-installer"),
		ReleaseImage: "quay.io/openshift-release-dev/ocp-release@sha256:rrr",
		Sets:         sets,
	}
	if err := Extract(context.Background(), o); err != nil {
		t.Fatalf("release info 재시도 후 성공해야 합니다: %v", err)
	}
	log := readLog(t, logf)
	if !strings.Contains(log, "RELEASEINFO rel-a.local/release@sha256:rrr") ||
		!strings.Contains(log, "RELEASEINFO rel-b.local/release@sha256:rrr") {
		t.Errorf("release info가 두 후보를 시도해야 합니다:\n%s", log)
	}
}

// 미러 설정을 읽지 못하고 desired release 이미지가 인터넷 원본 주소이면,
// 다이제스트를 유지한 채 설정된 미러 저장소의 release-images 경로로 바꿔
// release info를 호출하는지 확인합니다.
func TestReleaseImageFallsBackWhenMirrorConfigUnreadable(t *testing.T) {
	dir := t.TempDir()
	pull := filepath.Join(dir, "pull.json")
	if err := os.WriteFile(pull, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "oc.log")
	t.Setenv(envFakeOC, "1")
	t.Setenv(envOCLog, logf)
	t.Setenv(envOCGetForbid, "1") // 미러 설정 조회 모두 Forbidden → 신뢰 불가
	// release info가 반환할 payload에도 미러 주소를 사용합니다.
	t.Setenv(envOCMOS, "mirror.local:8443/openshift/release@sha256:mmm")
	t.Setenv(envOCFailInfo, "quay.io") // 인터넷 원본으로 release info를 시도하면 실패

	sets, err := mirror.Load(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("mirror.Load: %v", err)
	}
	if sets.Reliable() {
		t.Fatal("모든 조회 실패 시 Reliable()은 false여야 합니다")
	}
	o := Options{
		OCPath: os.Args[0], Version: "4.22.5",
		Registry: "mirror.local:8443", BaseRegistry: "mirror.local:8443",
		PullSecret: pull, TmpDir: filepath.Join(dir, "tmp"),
		ISOTarget: filepath.Join(dir, "full.iso"), InstallerTgt: filepath.Join(dir, "coreos-installer"),
		ReleaseImage: "quay.io/openshift-release-dev/ocp-release@sha256:rrr",
		Sets:         sets,
	}
	if err := Extract(context.Background(), o); err != nil {
		t.Fatalf("release 이미지 폴백 추출이 성공해야 합니다: %v", err)
	}
	log := readLog(t, logf)
	if !strings.Contains(log, "RELEASEINFO mirror.local:8443/openshift/release-images@sha256:rrr") {
		t.Errorf("release info는 미러 release-images 주소로 폴백해야 합니다:\n%s", log)
	}
	if strings.Contains(log, "RELEASEINFO quay.io/openshift-release-dev/ocp-release@sha256:rrr") {
		t.Errorf("release info가 인터넷 원본을 시도하면 안 됩니다:\n%s", log)
	}
}
