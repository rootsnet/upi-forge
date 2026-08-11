package fsutil

// 롤백 실패 경로는 파일 시스템 오류를 실제로 일으키기 어려우므로,
// 패키지 내부 변수 restoreRename을 바꿔치기해 주입합니다.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("파일 생성 실패: %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("권한 설정 실패: %s: %v", path, err)
	}
}

// setupFailingRollback은 다음 상황을 만듭니다.
//
//	항목 1: 기존 파일이 있고 rename까지 성공 → 롤백에서 복원해야 함
//	항목 2: 대상 디렉터리가 없어 rename 실패 → 롤백 발동
//
// 여기에 더해 복원 rename이 실패하도록 주입합니다.
func setupFailingRollback(t *testing.T, restoreErr error) (dir, target string, commitErr error, repl *Replacement) {
	t.Helper()
	dir = t.TempDir()

	target = filepath.Join(dir, "coreos-installer")
	mustWrite(t, target, "원본 내용", 0o755)

	staged := filepath.Join(dir, ".stage-installer")
	mustWrite(t, staged, "새 내용", 0o644)
	stagedSecond := filepath.Join(dir, ".stage-second")
	mustWrite(t, stagedSecond, "새 내용 2", 0o644)

	original := restoreRename
	restoreRename = func(oldpath, newpath string) error {
		if newpath == target {
			return restoreErr
		}
		return original(oldpath, newpath)
	}
	t.Cleanup(func() { restoreRename = original })

	repl = NewReplacement()
	repl.Add(staged, target, 0o755)
	repl.Add(stagedSecond, filepath.Join(dir, "no-such-dir", "x.iso"), 0o644)

	commitErr = repl.Commit()
	return dir, target, commitErr, repl
}

// 복원에 실패하면 원본을 되살릴 수 있도록 백업을 반드시 남겨야 합니다.
func TestRollbackKeepsBackupWhenRestoreFails(t *testing.T) {
	injected := errors.New("주입한 복원 실패")
	dir, target, commitErr, repl := setupFailingRollback(t, injected)

	if commitErr == nil {
		t.Fatal("Commit은 실패해야 합니다")
	}
	repl.Cleanup()

	// 백업 파일이 남아 있어야 원본을 되살릴 수 있습니다.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upi-forge-backup-") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) == 0 {
		t.Fatalf("복원에 실패했으면 백업을 남겨야 합니다. 남은 파일: %v", entryNames(entries))
	}

	// 남은 백업으로 원본을 되살릴 수 있어야 합니다.
	data, err := os.ReadFile(filepath.Join(dir, backups[0]))
	if err != nil {
		t.Fatalf("백업을 읽지 못했습니다: %v", err)
	}
	if string(data) != "원본 내용" {
		t.Errorf("백업 내용이 원본과 다릅니다: %q", data)
	}

	// 원래 실패 이유와 롤백 실패가 함께 보고되어야 합니다.
	if !errors.Is(commitErr, injected) {
		t.Errorf("롤백 실패 원인이 오류에 포함되어야 합니다: %v", commitErr)
	}
	if !strings.Contains(commitErr.Error(), "새 파일로 교체하지 못했습니다") {
		t.Errorf("원래 실패 이유도 함께 보고되어야 합니다: %v", commitErr)
	}

	// 운영자가 손으로 복구할 수 있도록 백업 경로가 메시지에 있어야 합니다.
	if !strings.Contains(commitErr.Error(), backups[0]) {
		t.Errorf("백업 경로가 오류 메시지에 있어야 합니다:\n%v", commitErr)
	}
	if !strings.Contains(commitErr.Error(), target) {
		t.Errorf("복원 대상 경로가 오류 메시지에 있어야 합니다:\n%v", commitErr)
	}
}

// TestRollbackSucceedsNormally는 주입 없이 정상 롤백되면
// 백업이 정리되고 원본이 복원되는지 확인합니다.
func TestRollbackSucceedsNormally(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "coreos-installer")
	mustWrite(t, target, "원본 내용", 0o644)
	staged := filepath.Join(dir, ".stage-installer")
	mustWrite(t, staged, "새 내용", 0o644)
	stagedSecond := filepath.Join(dir, ".stage-second")
	mustWrite(t, stagedSecond, "새 내용 2", 0o644)

	repl := NewReplacement()
	repl.Add(staged, target, 0o644)
	repl.Add(stagedSecond, filepath.Join(dir, "no-such-dir", "x.iso"), 0o644)

	if err := repl.Commit(); err == nil {
		t.Fatal("Commit은 실패해야 합니다")
	}
	repl.Cleanup()

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("원본이 복원되지 않았습니다: %v", err)
	}
	if string(data) != "원본 내용" {
		t.Errorf("원본 내용이 복원되지 않았습니다: %q", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upi-forge-backup-") {
			t.Errorf("정상 복원 후에는 백업이 남지 않아야 합니다: %v", entryNames(entries))
			break
		}
	}
}

func entryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
