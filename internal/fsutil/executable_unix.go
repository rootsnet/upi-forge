//go:build !windows

package fsutil

import "os"

// hasExecutableMode는 실행 비트가 하나라도 켜져 있는지 확인합니다.
func hasExecutableMode(mode os.FileMode) bool {
	return mode.Perm()&0o111 != 0
}
