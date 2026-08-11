//go:build !windows

package execx

import "os"

// isExecutableFile은 경로가 실행 가능한 일반 파일인지 확인합니다.
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
