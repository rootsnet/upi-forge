//go:build !windows

package config_test

// 회귀 테스트: tmpDir의 중간 구성요소가 작업
// 디렉터리 밖을 가리키는 심볼릭 링크면 문자열 검사는 통과하지만,
// prepare의 os.RemoveAll이 링크 대상 아래를 삭제하게 됩니다. 링크를
// 해석한 뒤의 실제 위치가 작업 디렉터리 밖이면 거부해야 합니다.
// (심볼릭 링크 생성이 필요해 Windows에서는 실행하지 않습니다 —
// 실행 대상은 Linux bastion입니다.)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/config"
)

func symlinkConfig(t *testing.T, workspaceDir, tmpDir string) *config.Config {
	t.Helper()
	content := strings.Replace(minimalConfig, "pathsets:",
		"workspace:\n  dir: '"+workspaceDir+"'\n  tmpDir: \""+tmpDir+"\"\npathsets:", 1)
	root := writeTree(t, map[string]string{"upi-forge.yaml": content})
	cfg, err := config.Load(filepath.Join(root, "upi-forge.yaml"))
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	return cfg
}

func TestPathsRejectsSymlinkedTmpDirOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "work")); err != nil {
		t.Fatalf("심볼릭 링크 생성 실패: %v", err)
	}

	cfg := symlinkConfig(t, workspace, "work/tmp")
	_, err := cfg.Paths()
	if err == nil || !strings.Contains(err.Error(), "밖을 가리킵니다") {
		t.Fatalf("링크로 밖을 가리키는 tmpDir는 거부되어야 합니다: %v", err)
	}
}

// 작업 디렉터리 자체가 심볼릭 링크 뒤에 있는 것은 정상입니다
// (양쪽을 같이 해석하므로 상대 관계가 유지됩니다).
func TestPathsAllowsSymlinkedWorkspaceItself(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("심볼릭 링크 생성 실패: %v", err)
	}

	cfg := symlinkConfig(t, link, "tmp")
	paths, err := cfg.Paths()
	if err != nil {
		t.Fatalf("링크 뒤의 작업 디렉터리는 정상이어야 합니다: %v", err)
	}
	if paths.TmpDir != filepath.Join(link, "tmp") {
		t.Errorf("TmpDir: %q", paths.TmpDir)
	}
}

// 작업 디렉터리 안의 내부 링크(안→안)는 정상입니다.
func TestPathsAllowsInternalSymlink(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, "real"), filepath.Join(workspace, "work")); err != nil {
		t.Fatalf("심볼릭 링크 생성 실패: %v", err)
	}

	cfg := symlinkConfig(t, workspace, "work/tmp")
	if _, err := cfg.Paths(); err != nil {
		t.Fatalf("작업 디렉터리 안을 가리키는 링크는 정상이어야 합니다: %v", err)
	}
}
