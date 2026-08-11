//go:build !windows

// Unix 파일 권한(실행 비트, 0600 같은 정확한 mode)에 의존하는 검증입니다.
// Windows는 실행 비트가 없고 chmod가 읽기 전용 플래그로만 매핑되므로
// 같은 단언을 그대로 쓸 수 없습니다. upi-forge의 실제 실행 대상은
// Linux bastion이므로 이 부분은 Unix에서만 검증합니다.
// Windows에서 확인 가능한 동작(내용 교체, 롤백, 임시 파일 정리)은
// fsutil_test.go에 플랫폼 공통으로 두었습니다.

package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"upi-forge/internal/fsutil"
)

func TestWriteFileAtomicAppliesPermissionUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.ign")
	if err := fsutil.WriteFileAtomic(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic 실패: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Ignition에는 클러스터 연결 정보가 들어가므로 0600으로 만들어야 합니다.
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("권한이 다릅니다: got=%v want=0600", got)
	}
}

func TestReplacementSetsExecutableBitUnix(t *testing.T) {
	dir := t.TempDir()
	installer := filepath.Join(dir, "coreos-installer")
	writeFile(t, installer, "old-installer", 0o755)

	staged := filepath.Join(dir, ".stage-installer")
	writeFile(t, staged, "new-installer", 0o644)

	repl := fsutil.NewReplacement()
	repl.Add(staged, installer, 0o755)
	if err := repl.Commit(); err != nil {
		t.Fatalf("Commit 실패: %v", err)
	}
	repl.Cleanup()

	if !fsutil.IsExecutable(installer) {
		t.Error("교체한 coreos-installer가 실행 가능해야 합니다")
	}
}

// 롤백은 기존 실행 파일의 권한을 그대로 복원해야 합니다. 0600으로 되돌리면
// coreos-installer의 실행 권한이 사라져 다음 단계가 모두 실패합니다.
func TestReplacementRestoresExecutableBitOnRollbackUnix(t *testing.T) {
	dir := t.TempDir()

	installer := filepath.Join(dir, "coreos-installer")
	writeFile(t, installer, "old-installer", 0o755)
	stageInstaller := filepath.Join(dir, ".stage-installer")
	writeFile(t, stageInstaller, "new-installer", 0o644)

	// 두 번째 항목은 rename 대상 디렉터리가 없어 실패합니다.
	// 그 시점에는 첫 번째 항목의 rename이 이미 끝나 있습니다.
	stageSecond := filepath.Join(dir, ".stage-second")
	writeFile(t, stageSecond, "new-second", 0o644)

	repl := fsutil.NewReplacement()
	repl.Add(stageInstaller, installer, 0o755)
	repl.Add(stageSecond, filepath.Join(dir, "no-such-dir", "x.iso"), 0o644)

	if err := repl.Commit(); err == nil {
		t.Fatal("rename이 실패하면 오류여야 합니다")
	}
	repl.Cleanup()

	if got := readFile(t, installer); got != "old-installer" {
		t.Fatalf("기존 내용이 복구되지 않았습니다: %q", got)
	}
	info, err := os.Stat(installer)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("원래 권한으로 복구되어야 합니다: got=%v want=0755", got)
	}
	if !fsutil.IsExecutable(installer) {
		t.Error("복구한 coreos-installer가 실행 가능해야 합니다")
	}
}

// TestReplacementRestoresReadOnlyModeUnix는 실행 비트 외의 권한도
// 원래대로 돌아오는지 확인합니다.
func TestReplacementRestoresReadOnlyModeUnix(t *testing.T) {
	dir := t.TempDir()

	iso := filepath.Join(dir, "coreos-x86_64.iso")
	writeFile(t, iso, "old-iso", 0o444)
	staged := filepath.Join(dir, ".stage-iso")
	writeFile(t, staged, "new-iso", 0o644)

	stageSecond := filepath.Join(dir, ".stage-second")
	writeFile(t, stageSecond, "new-second", 0o644)

	repl := fsutil.NewReplacement()
	repl.Add(staged, iso, 0o644)
	repl.Add(stageSecond, filepath.Join(dir, "no-such-dir", "x.iso"), 0o644)

	if err := repl.Commit(); err == nil {
		t.Fatal("rename이 실패하면 오류여야 합니다")
	}
	repl.Cleanup()

	info, err := os.Stat(iso)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Errorf("원래 권한으로 복구되어야 합니다: got=%v want=0444", got)
	}
	if got := readFile(t, iso); got != "old-iso" {
		t.Errorf("원래 내용으로 복구되어야 합니다: %q", got)
	}
}
