package mirror

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// 응답하지 않는 oc get을 제한 시간에 맞춰 종료하고, 오류 없이 빈 Sets를
// 반환하는지 확인합니다. 상위 컨텍스트 취소와는 구분해 처리합니다.
func TestLoadTimesOutAndFallsBack(t *testing.T) {
	t.Setenv(envFakeOCMirror, "1")
	t.Setenv(envHang, "1")
	orig := queryTimeout
	queryTimeout = 150 * time.Millisecond
	t.Cleanup(func() { queryTimeout = orig })

	start := time.Now()
	sets, err := Load(context.Background(), os.Args[0])
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("제한 시간 초과는 오류가 아니라 폴백이어야 합니다: %v", err)
	}
	if !sets.Empty() {
		t.Errorf("조회 실패 시 빈 Sets여야 합니다")
	}
	if elapsed > 5*time.Second {
		t.Errorf("제한 시간 안에 끝나야 합니다: %s", elapsed)
	}
}

// 상위 컨텍스트가 취소되면 Load가 취소 오류를 호출부에 반환하는지 확인합니다.
func TestLoadRespectsCancellation(t *testing.T) {
	t.Setenv(envFakeOCMirror, "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, os.Args[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("취소는 오류로 중단해야 합니다: %v", err)
	}
}

// 조회가 권한 오류로 실패하면 Sets.Reliable이 false를 반환하는지 확인합니다.
func TestLoadUnreliableOnForbidden(t *testing.T) {
	t.Setenv(envFakeOCMirror, "1")
	t.Setenv(envForbid, "1")
	sets, err := Load(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("권한 오류는 폴백이어야 합니다(취소 아님): %v", err)
	}
	if sets.Reliable() {
		t.Error("조회 실패 시 Reliable()은 false여야 합니다")
	}
	if !sets.Empty() {
		t.Error("조회 실패 시 빈 Sets여야 합니다")
	}
}

// 모든 리소스를 정상 조회하면 목록이 비어 있어도 Sets.Reliable이 true인지 확인합니다.
func TestLoadReliableWhenAllRead(t *testing.T) {
	t.Setenv(envFakeOCMirror, "1")
	sets, err := Load(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("Load 오류: %v", err)
	}
	if !sets.Reliable() {
		t.Error("모두 정상 조회하면 Reliable()은 true여야 합니다")
	}
}
