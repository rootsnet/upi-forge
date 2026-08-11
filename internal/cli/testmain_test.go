package cli

// TestMain은 coreos-installer 대역 역할을 겸합니다.
//
// ignition 패키지의 butane 대역과 같은 방식입니다. 환경 변수가 설정된 채
// 실행되면 테스트 바이너리가 coreos-installer처럼 동작해, `-o` 뒤의 경로에
// 비어 있지 않은 출력 파일을 만들고 종료합니다. 셸 스크립트 대역과 달리
// Windows에서도 동작합니다.

import (
	"fmt"
	"os"
	"testing"
)

const envFakeInstaller = "UPI_FORGE_TEST_FAKE_INSTALLER"

func TestMain(m *testing.M) {
	if os.Getenv(envFakeInstaller) == "1" {
		for i, arg := range os.Args {
			if arg == "-o" && i+1 < len(os.Args) {
				if err := os.WriteFile(os.Args[i+1], []byte("fake-node-iso"), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}
		}
		fmt.Println("Boot media will automatically install without confirmation.")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
