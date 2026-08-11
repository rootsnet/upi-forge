package fsutil_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"upi-forge/internal/fsutil"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("파일 생성 실패: %s: %v", path, err)
	}
	// os.WriteFile은 umask의 영향을 받으므로 권한을 명시적으로 맞춥니다.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("권한 설정 실패: %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("파일 읽기 실패: %s: %v", path, err)
	}
	return string(data)
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestMustNotExist(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "new.ign")
	if err := fsutil.MustNotExist(missing); err != nil {
		t.Errorf("없는 파일은 통과해야 합니다: %v", err)
	}

	existing := filepath.Join(dir, "old.ign")
	writeFile(t, existing, "{}", 0o600)
	if err := fsutil.MustNotExist(existing); !errors.Is(err, fsutil.ErrExists) {
		t.Errorf("기존 파일은 ErrExists여야 합니다: %v", err)
	}
}

func TestWriteFileIfSameOrNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker1.yaml")
	data := []byte("interfaces:\n")

	reused, err := fsutil.WriteFileIfSameOrNew(path, data, 0o644)
	if err != nil || reused {
		t.Fatalf("첫 생성: reused=%t err=%v", reused, err)
	}

	reused, err = fsutil.WriteFileIfSameOrNew(path, data, 0o644)
	if err != nil || !reused {
		t.Fatalf("내용이 같으면 재사용해야 합니다: reused=%t err=%v", reused, err)
	}

	_, err = fsutil.WriteFileIfSameOrNew(path, []byte("interfaces: changed\n"), 0o644)
	if !errors.Is(err, fsutil.ErrContentDiffers) {
		t.Fatalf("내용이 다르면 ErrContentDiffers여야 합니다: %v", err)
	}
	if got := readFile(t, path); got != string(data) {
		t.Errorf("기존 파일이 변경되었습니다: %q", got)
	}
}

func TestWriteFileAtomicLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.ign")
	if err := fsutil.WriteFileAtomic(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic 실패: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.ign" {
		t.Errorf("임시 파일이 남았습니다: %v", names(entries))
	}
	if got := readFile(t, path); got != "{}" {
		t.Errorf("내용이 다릅니다: %q", got)
	}
}

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	writeFile(t, a, "same", 0o600)
	writeFile(t, b, "same", 0o600)

	same, err := fsutil.SameContent(a, b)
	if err != nil || !same {
		t.Errorf("같은 내용이어야 합니다: %t %v", same, err)
	}
	writeFile(t, b, "diff", 0o600)
	same, err = fsutil.SameContent(a, b)
	if err != nil || same {
		t.Errorf("다른 내용이어야 합니다: %t %v", same, err)
	}
}

func TestReplacementCommitReplacesAllAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.Join(dir, "coreos-x86_64.iso")
	installer := filepath.Join(dir, "coreos-installer")
	writeFile(t, iso, "old-iso", 0o644)
	writeFile(t, installer, "old-installer", 0o644)

	stageISO := filepath.Join(dir, ".stage-iso")
	stageInstaller := filepath.Join(dir, ".stage-installer")
	writeFile(t, stageISO, "new-iso", 0o644)
	writeFile(t, stageInstaller, "new-installer", 0o644)

	// 실행 비트 검증은 Unix 전용 테스트에서 다룹니다.
	// 여기서는 플랫폼과 무관한 "내용 교체와 정리"만 확인합니다.
	repl := fsutil.NewReplacement()
	repl.Add(stageISO, iso, 0o644)
	repl.Add(stageInstaller, installer, 0o644)
	if err := repl.Commit(); err != nil {
		t.Fatalf("Commit 실패: %v", err)
	}
	repl.Cleanup()

	if got := readFile(t, iso); got != "new-iso" {
		t.Errorf("ISO가 교체되지 않았습니다: %q", got)
	}
	if got := readFile(t, installer); got != "new-installer" {
		t.Errorf("installer가 교체되지 않았습니다: %q", got)
	}
	// 백업과 staged 파일이 모두 정리되어 최종 산출물 두 개만 남아야 합니다.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("백업 또는 staged 파일이 남았습니다: %v", names(entries))
	}
}

func TestReplacementRollsBackWhenStagedMissing(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.Join(dir, "coreos-x86_64.iso")
	writeFile(t, iso, "old-iso", 0o644)
	stageISO := filepath.Join(dir, ".stage-iso")
	writeFile(t, stageISO, "new-iso", 0o644)

	// 두 번째 항목의 staged 파일이 없으므로 Commit은 시작 전에 실패해야 하고,
	// 첫 번째 항목은 손대지 않아야 합니다.
	repl := fsutil.NewReplacement()
	repl.Add(stageISO, iso, 0o644)
	repl.Add(filepath.Join(dir, ".missing"), filepath.Join(dir, "coreos-installer"), 0o755)

	if err := repl.Commit(); err == nil {
		t.Fatal("staged 파일이 없으면 실패해야 합니다")
	}
	repl.Cleanup()

	if got := readFile(t, iso); got != "old-iso" {
		t.Errorf("기존 ISO가 복구되지 않았습니다: %q", got)
	}
}

// 교체 전에 없던 target은 롤백 시 반드시 삭제되어야 합니다.
// 남아 있으면 다음 실행이 "이미 있습니다"로 막히거나,
// 반쪽짜리 산출물이 정상인 것처럼 남습니다.
func TestReplacementRemovesNewTargetsOnRollback(t *testing.T) {
	dir := t.TempDir()

	// 첫 번째 target은 기존에 없던 새 파일이며 rename까지 성공합니다.
	newISO := filepath.Join(dir, "coreos-x86_64.iso")
	stageISO := filepath.Join(dir, ".stage-iso")
	writeFile(t, stageISO, "new-iso", 0o644)

	// 두 번째 항목은 대상 디렉터리가 없어 rename에서 실패합니다.
	// 이때 첫 번째 항목은 이미 만들어져 있으므로 롤백이 지워야 합니다.
	stageSecond := filepath.Join(dir, ".stage-second")
	writeFile(t, stageSecond, "new-second", 0o644)

	repl := fsutil.NewReplacement()
	repl.Add(stageISO, newISO, 0o644)
	repl.Add(stageSecond, filepath.Join(dir, "no-such-dir", "coreos-installer"), 0o755)

	if err := repl.Commit(); err == nil {
		t.Fatal("rename이 실패하면 오류여야 합니다")
	}
	repl.Cleanup()

	if fsutil.Exists(newISO) {
		t.Errorf("교체 전에 없던 target이 롤백 후에도 남았습니다: %s", newISO)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("롤백 후 아무 파일도 남지 않아야 합니다: %v", names(entries))
	}
}

// TestReplacementRollsBackAfterPartialRename은 rename이 일부 끝난 뒤
// 검증 단계에서 실패했을 때 모든 항목이 원상 복구되는지 확인합니다.
func TestReplacementRollsBackAfterPartialRename(t *testing.T) {
	dir := t.TempDir()

	first := filepath.Join(dir, "first.iso")
	writeFile(t, first, "old-first", 0o644)
	stageFirst := filepath.Join(dir, ".stage-first")
	writeFile(t, stageFirst, "new-first", 0o644)

	// 두 번째는 기존에 없던 target이고, rename 대상 디렉터리가 없어 실패합니다.
	missingDir := filepath.Join(dir, "no-such-dir", "second.iso")
	stageSecond := filepath.Join(dir, ".stage-second")
	writeFile(t, stageSecond, "new-second", 0o644)

	repl := fsutil.NewReplacement()
	repl.Add(stageFirst, first, 0o644)
	repl.Add(stageSecond, missingDir, 0o644)

	if err := repl.Commit(); err == nil {
		t.Fatal("rename이 실패하면 오류여야 합니다")
	}
	repl.Cleanup()

	if got := readFile(t, first); got != "old-first" {
		t.Errorf("첫 번째 항목이 복구되지 않았습니다: %q", got)
	}
	if fsutil.Exists(missingDir) {
		t.Errorf("두 번째 항목이 만들어졌습니다: %s", missingDir)
	}
}
