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
	"os/signal"
	"testing"
)

const envFakeInstaller = "UPI_FORGE_TEST_FAKE_INSTALLER"

// envInterruptHelper가 설정되면 테스트 바이너리는 "인터럽트를 받으면 정리
// 후 종료하는 하위 프로세스" 역할을 합니다. launchWorker의 취소 동작
// 테스트(parallel_interrupt_unix_test.go)가 실제 프로세스로 확인하는 데
// 사용합니다.
const envInterruptHelper = "UPI_FORGE_TEST_INTERRUPT_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(envInterruptHelper) == "1" {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		fmt.Println("helper-ready")
		<-sig
		// 인터럽트 후에도 정리 코드가 실행되는 상황을 재현합니다.
		// 즉시 강제 종료되면 이 줄이 출력되지 못합니다.
		fmt.Println("helper-cleanup-done")
		os.Exit(0)
	}
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
