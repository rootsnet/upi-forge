package release

// TestMain은 테스트 바이너리를 다시 실행해 Windows에서도 사용할 수 있는
// oc 대역을 제공합니다.
//
// 환경 변수로 동작을 제어합니다.
//   UPI_FORGE_TEST_FAKE_OC=1  : 대역 모드로 진입
//   ..._OC_LOG                : 호출을 한 줄씩 기록할 파일
//   ..._OC_MOS                : `adm release info`가 출력할 machine-os-images 참조
//   ..._OC_IDMS               : `get imagedigestmirrorset -o json` 출력(JSON)
//   ..._OC_FAIL               : image extract에서 실패시킬 이미지 접두사(쉼표 구분)

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const (
	envFakeOC      = "UPI_FORGE_TEST_FAKE_OC"
	envOCLog       = "UPI_FORGE_TEST_OC_LOG"
	envOCMOS       = "UPI_FORGE_TEST_OC_MOS"
	envOCIDMS      = "UPI_FORGE_TEST_OC_IDMS"
	envOCFail      = "UPI_FORGE_TEST_OC_FAIL"
	envOCFailInfo  = "UPI_FORGE_TEST_OC_FAIL_INFO"
	envOCGetForbid = "UPI_FORGE_TEST_OC_GET_FORBID"
)

func TestMain(m *testing.M) {
	if os.Getenv(envFakeOC) != "1" {
		os.Exit(m.Run())
	}
	os.Exit(fakeOC(os.Args[1:]))
}

func fakeOC(args []string) int {
	appendLog := func(line string) {
		if p := os.Getenv(envOCLog); p != "" {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintln(f, line)
				f.Close()
			}
		}
	}
	has := func(s string) bool {
		for _, a := range args {
			if a == s {
				return true
			}
		}
		return false
	}
	nextNonFlag := func(after string) string {
		seen := false
		for _, a := range args {
			if seen && !strings.HasPrefix(a, "-") {
				return a
			}
			if a == after {
				seen = true
			}
		}
		return ""
	}

	switch {
	case has("info"): // adm release info <ref> --image-for=...
		ref := nextNonFlag("info")
		appendLog("RELEASEINFO " + ref)
		for _, pfx := range strings.Split(os.Getenv(envOCFailInfo), ",") {
			if pfx != "" && strings.HasPrefix(ref, pfx) {
				fmt.Fprintln(os.Stderr, "fake oc: release info 실패: "+ref)
				return 1
			}
		}
		fmt.Println(os.Getenv(envOCMOS))
		return 0

	case has("extract"): // image extract <ref> --path=/:DIR ...
		ref := nextNonFlag("extract")
		appendLog("EXTRACT " + ref)
		for _, pfx := range strings.Split(os.Getenv(envOCFail), ",") {
			if pfx != "" && strings.HasPrefix(ref, pfx) {
				fmt.Fprintln(os.Stderr, "fake oc: 이미지를 당길 수 없습니다: "+ref)
				return 1
			}
		}
		var dir string
		for _, a := range args {
			if strings.HasPrefix(a, "--path=/:") {
				dir = strings.TrimPrefix(a, "--path=/:")
			}
		}
		if dir != "" {
			_ = os.WriteFile(dir+string(os.PathSeparator)+"rhcos-live.iso", []byte("iso"), 0o644)
			_ = os.WriteFile(dir+string(os.PathSeparator)+"coreos-installer", []byte("inst"), 0o755)
		}
		return 0

	case has("get"): // get <resource> -o json (미러 설정)
		if os.Getenv(envOCGetForbid) == "1" {
			fmt.Fprintln(os.Stderr, "Error from server (Forbidden): ... is forbidden")
			return 1
		}
		switch nextNonFlag("get") {
		case "imagedigestmirrorset":
			if v := os.Getenv(envOCIDMS); v != "" {
				fmt.Println(v)
				return 0
			}
		}
		fmt.Println(`{"items":[]}`)
		return 0
	}
	return 0
}
