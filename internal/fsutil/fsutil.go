// Package fsutil은 산출물을 안전하게 생성하고 교체하기 위한 파일 유틸리티를
// 제공합니다. 주요 안전 규칙은 다음과 같습니다.
//
//   - 기존 Ignition과 ISO는 절대 덮어쓰지 않는다.
//   - NMState YAML은 내용이 같을 때만 재사용하고, 다르면 중단한다.
//   - full ISO와 coreos-installer는 교체 전에 백업하고, 실패하면 되돌린다.
//   - 파일은 임시 파일에 다 쓴 뒤 rename으로 원자적으로 옮긴다.
package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrExists는 덮어쓰기 금지 대상이 이미 존재할 때 반환합니다.
var ErrExists = errors.New("출력 파일이 이미 있습니다")

// ErrContentDiffers는 재사용 대상 파일의 내용이 달라 덮어쓸 수 없을 때 반환합니다.
var ErrContentDiffers = errors.New("기존 파일의 내용이 다릅니다")

// Exists는 경로가 존재하는지 확인합니다. 심볼릭 링크 자체도 존재로 봅니다.
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// IsRegularFile은 경로가 심볼릭 링크가 아닌 일반 파일인지 확인합니다.
func IsRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// MustNotExist는 덮어쓰기 금지 대상이 이미 있으면 오류를 반환합니다.
func MustNotExist(path string) error {
	if Exists(path) {
		return fmt.Errorf("%w(덮어쓰지 않음): %s", ErrExists, path)
	}
	return nil
}

// IsExecutable은 실행 가능한 일반 파일인지 확인합니다.
// 실행 비트 판정은 플랫폼마다 다르므로 executable_*.go로 나눠 두었습니다.
func IsExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return hasExecutableMode(info.Mode())
}

// IsNonEmptyFile은 크기가 0보다 큰 일반 파일인지 확인합니다.
func IsNonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// SameContent는 두 파일의 내용이 같은지 비교합니다.
func SameContent(a, b string) (bool, error) {
	left, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	right, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}

// WriteFileAtomic은 같은 디렉터리의 임시 파일에 쓴 뒤 rename으로 교체합니다.
// 도중에 실패하면 대상 경로는 손대지 않은 상태로 남습니다.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("디렉터리를 만들지 못했습니다: %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".upi-forge-*.tmp")
	if err != nil {
		return fmt.Errorf("임시 파일을 만들지 못했습니다: %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("임시 파일에 쓰지 못했습니다: %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("임시 파일을 동기화하지 못했습니다: %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("임시 파일을 닫지 못했습니다: %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("임시 파일 권한을 설정하지 못했습니다: %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("파일을 교체하지 못했습니다: %s: %w", path, err)
	}
	return nil
}

// WriteFileIfSameOrNew는 파일이 없으면 새로 만들고, 이미 있고 내용이 같으면
// 그대로 두고 reused=true를 반환합니다. 내용이 다르면 ErrContentDiffers로
// 중단합니다.
func WriteFileIfSameOrNew(path string, data []byte, perm os.FileMode) (reused bool, err error) {
	if Exists(path) {
		existing, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("기존 파일을 읽지 못했습니다: %s: %w", path, err)
		}
		if bytes.Equal(existing, data) {
			return true, nil
		}
		return false, fmt.Errorf("%w(덮어쓰지 않음): %s", ErrContentDiffers, path)
	}
	return false, WriteFileAtomic(path, data, perm)
}

// CopyFile은 파일 내용을 복사합니다. 대상이 이미 있으면 덮어씁니다.
func CopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("원본을 열지 못했습니다: %s: %w", src, err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("대상 디렉터리를 만들지 못했습니다: %s: %w", filepath.Dir(dst), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upi-forge-*.tmp")
	if err != nil {
		return fmt.Errorf("임시 파일을 만들지 못했습니다: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("파일을 복사하지 못했습니다: %s -> %s: %w", src, dst, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("임시 파일을 동기화하지 못했습니다: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("임시 파일을 닫지 못했습니다: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("임시 파일 권한을 설정하지 못했습니다: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("파일을 교체하지 못했습니다: %s: %w", dst, err)
	}
	return nil
}

// Replacement는 여러 산출물을 모두 교체하거나 모두 원래 상태로 되돌리는
// 트랜잭션형 도우미입니다.
//
//	r := fsutil.NewReplacement()
//	defer r.Cleanup()
//	r.Add(stagedISO, targetISO, 0o644)
//	r.Add(stagedInstaller, targetInstaller, 0o755)
//	if err := r.Commit(); err != nil { ... }  // 실패 시 자동 롤백
type Replacement struct {
	items     []replacementItem
	committed bool
}

type replacementItem struct {
	staged string // 새로 만들어 둔 파일(교체 후 사라짐)
	target string // 최종 경로
	perm   os.FileMode

	// 아래 값들은 Commit이 채웁니다. 롤백이 "교체 전 상태"를 정확히
	// 되돌리려면 내용뿐 아니라 존재 여부와 원래 권한까지 알아야 합니다.
	existed  bool        // 교체 전에 target이 있었는지
	origMode os.FileMode // 교체 전 target의 권한 (existed일 때만 유효)
	backup   string      // 기존 내용 백업 경로 (existed일 때만)
	replaced bool        // 이미 rename까지 끝났는지

	// restoreFailed는 롤백에서 원본 복원에 실패했다는 표시입니다.
	// 이 경우 backup 파일이 원본의 유일한 사본이므로 Cleanup이 지우면 안 됩니다.
	restoreFailed bool
}

// restoreRename은 롤백에서 백업을 되돌릴 때 사용합니다.
// 테스트에서 복원 실패를 주입할 수 있도록 변수로 둡니다.
var restoreRename = os.Rename

// NewReplacement는 빈 교체 묶음을 만듭니다.
func NewReplacement() *Replacement { return &Replacement{} }

// Add는 교체 대상 하나를 등록합니다.
func (r *Replacement) Add(staged, target string, perm os.FileMode) {
	r.items = append(r.items, replacementItem{staged: staged, target: target, perm: perm})
}

// Commit은 등록한 모든 파일을 교체합니다. 하나라도 실패하면 이미 교체한
// 파일까지 모두 원래 내용으로 되돌리고 오류를 반환합니다.
func (r *Replacement) Commit() error {
	// 1) 교체 전 상태를 확인하고 기록합니다.
	//    존재 여부와 원래 권한을 여기서 잡아 두어야 롤백이 정확해집니다.
	for i := range r.items {
		item := &r.items[i]
		info, err := os.Lstat(item.target)
		switch {
		case err == nil && !info.Mode().IsRegular():
			return fmt.Errorf("출력 경로가 일반 파일이 아닙니다: %s", item.target)
		case err == nil:
			item.existed = true
			item.origMode = info.Mode().Perm()
		case !os.IsNotExist(err):
			return fmt.Errorf("출력 경로를 확인하지 못했습니다: %s: %w", item.target, err)
		}
		if !IsNonEmptyFile(item.staged) {
			return fmt.Errorf("교체할 새 파일이 비어 있습니다: %s", item.staged)
		}
	}

	// 2) 기존 파일을 원래 권한 그대로 백업합니다.
	//    0600으로 백업하면 복구 시 실행 권한이 사라집니다.
	for i := range r.items {
		item := &r.items[i]
		if !item.existed {
			continue
		}
		backup, err := os.CreateTemp(filepath.Dir(item.target), ".upi-forge-backup-*")
		if err != nil {
			return r.fail(fmt.Errorf("기존 파일을 백업하지 못했습니다: %s: %w", item.target, err))
		}
		backup.Close()
		if err := CopyFile(item.target, backup.Name(), item.origMode); err != nil {
			os.Remove(backup.Name())
			return r.fail(fmt.Errorf("기존 파일을 백업하지 못했습니다: %s: %w", item.target, err))
		}
		item.backup = backup.Name()
	}

	// 3) 새 파일로 교체합니다.
	for i := range r.items {
		item := &r.items[i]
		if err := os.Chmod(item.staged, item.perm); err != nil {
			return r.fail(fmt.Errorf("새 파일 권한을 설정하지 못했습니다: %s: %w", item.staged, err))
		}
		if err := os.Rename(item.staged, item.target); err != nil {
			return r.fail(fmt.Errorf("새 파일로 교체하지 못했습니다: %s: %w", item.target, err))
		}
		item.replaced = true
	}

	// 4) 교체 결과를 검증합니다.
	for i := range r.items {
		item := &r.items[i]
		if !IsNonEmptyFile(item.target) {
			return r.fail(fmt.Errorf("교체한 파일이 비어 있습니다: %s", item.target))
		}
		if item.perm&0o111 != 0 && !IsExecutable(item.target) {
			return r.fail(fmt.Errorf("교체한 파일을 실행할 수 없습니다: %s", item.target))
		}
	}

	r.committed = true
	return nil
}

// fail은 교체 실패 시 롤백까지 수행하고, 원래 오류와 롤백 오류를 함께 반환합니다.
//
// 롤백이 실패하면 운영자가 손으로 복구해야 하므로, 원래 실패 이유와
// 복구에 필요한 백업 파일 위치를 모두 남깁니다.
func (r *Replacement) fail(cause error) error {
	if err := r.rollback(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// rollback은 교체 전 상태로 되돌립니다.
//
//   - 기존에 있던 파일은 원래 내용과 원래 권한으로 복원합니다.
//   - 기존에 없던 파일은 삭제합니다. 그래야 "모두 성공하거나 모두 원래대로"가
//     성립합니다. 이 처리가 없으면 뒤쪽 항목이 실패했을 때 앞쪽에서 새로 생긴
//     파일이 남아, 다음 실행이 "이미 있습니다"로 막히거나 반쪽짜리 산출물이
//     정상인 것처럼 남습니다.
//
// 복원은 백업을 대상 위로 직접 rename합니다. 대상을 먼저 지운 뒤 rename하면,
// rename이 실패하는 순간 원본이 사라진 상태가 됩니다.
// 복원에 실패한 항목은 restoreFailed로 표시해 Cleanup이 백업을 지우지 못하게 합니다.
// 그 백업이 원본의 유일한 사본이기 때문입니다.
func (r *Replacement) rollback() error {
	var errs []error

	// 나중에 바꾼 것부터 되돌립니다.
	for i := len(r.items) - 1; i >= 0; i-- {
		item := &r.items[i]
		if !item.replaced {
			continue
		}

		// 교체 전에 없던 파일은 지우기만 하면 됩니다.
		if !item.existed {
			if err := os.Remove(item.target); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("새로 만든 파일을 지우지 못했습니다: %s: %w", item.target, err))
				continue
			}
			item.replaced = false
			continue
		}

		if item.backup == "" {
			// 여기에 오면 안 됩니다. 백업 없이 기존 파일을 덮어쓴 상태이므로 알립니다.
			item.restoreFailed = true
			errs = append(errs, fmt.Errorf("백업이 없어 원본을 복원할 수 없습니다: %s", item.target))
			continue
		}

		// 백업을 대상 위로 직접 덮어씁니다(POSIX rename은 원자적으로 교체합니다).
		err := restoreRename(item.backup, item.target)
		if err != nil {
			// 대상이 있으면 rename이 실패하는 플랫폼을 위한 재시도입니다.
			if removeErr := os.Remove(item.target); removeErr == nil {
				err = restoreRename(item.backup, item.target)
			}
		}
		if err != nil {
			item.restoreFailed = true
			errs = append(errs, fmt.Errorf(
				"원본을 복원하지 못했습니다. 백업을 지우지 않았으니 직접 복구하세요: 백업=%s -> 대상=%s: %w",
				item.backup, item.target, err))
			continue
		}

		item.backup = ""
		if err := os.Chmod(item.target, item.origMode); err != nil {
			errs = append(errs, fmt.Errorf("복원한 파일의 권한을 되돌리지 못했습니다: %s: %w", item.target, err))
		}
		item.replaced = false
	}
	return errors.Join(errs...)
}

// Cleanup은 성공 시 백업을, 실패 시 남은 staged 파일을 정리합니다.
// 롤백된 항목의 staged 파일은 rename으로 이미 사라졌으므로 Remove가 조용히 넘어갑니다.
// defer로 호출하는 것을 전제로 합니다.
func (r *Replacement) Cleanup() {
	for i := range r.items {
		item := &r.items[i]
		// 복원에 실패한 항목의 백업은 원본의 유일한 사본이므로 절대 지우지 않습니다.
		if item.backup != "" && !item.restoreFailed {
			_ = os.Remove(item.backup)
			item.backup = ""
		}
		if !r.committed {
			_ = os.Remove(item.staged)
		}
	}
}
