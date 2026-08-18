//go:build !windows

package cli

// 취소(Ctrl+C) 시 launchWorker가 하위 프로세스를 즉시 강제 종료하지 않고
// 인터럽트를 보내 정리 코드를 마칠 기회를 주는지 실제 프로세스로
// 확인합니다. 하위 프로세스는 이 테스트 바이너리 자신을 도우미 모드로
// 실행한 것입니다(testmain_test.go의 envInterruptHelper 참고).
//
// Windows는 다른 프로세스에 인터럽트를 보낼 수 없어 즉시 강제 종료로
// 동작하므로(launchWorker 주석 참고) 이 테스트는 유닉스 전용입니다.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchWorkerInterruptGivesChildTimeToCleanUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logPath := filepath.Join(t.TempDir(), "w1.log")
	env := append(os.Environ(), envInterruptHelper+"=1")

	done := make(chan error, 1)
	go func() {
		done <- launchWorker(ctx, []string{"helper"}, env, logPath)
	}()

	// 도우미가 시그널 핸들러를 등록한 뒤에 취소해야 합니다.
	waitForLogMarker(t, logPath, "helper-ready")
	cancel()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("취소 후 하위 프로세스가 제한 시간 안에 끝나야 합니다")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("로그를 읽지 못했습니다: %v", err)
	}
	if !strings.Contains(string(data), "helper-cleanup-done") {
		t.Errorf("취소 시 하위 프로세스의 정리 코드가 실행되어야 합니다"+
			" (즉시 강제 종료 의심). 로그:\n%s", data)
	}
}

func waitForLogMarker(t *testing.T, path, marker string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), marker) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("로그에 %q가 나타나지 않았습니다: %s", marker, path)
}
