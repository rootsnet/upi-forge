// Command upi-forge는 OpenShift UPI 방식으로 worker 노드를 추가하는 작업을
// 자동화합니다. RHCOS 이미지 준비부터 노드별 Ignition/ISO 생성, iDRAC10
// Virtual Media 부팅까지 한 바이너리로 수행합니다.
//
// 사용법은 `upi-forge -h`를 참고하세요.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"upi-forge/internal/cli"
)

func main() {
	// Ctrl+C나 SIGTERM을 받으면 진행 중인 요청을 취소하고,
	// 각 단계의 defer가 iDRAC 세션과 임시 파일을 정리하도록 합니다.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	if err := cli.Execute(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\n중단되었습니다.")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "오류: %v\n", err)
		os.Exit(1)
	}
}
