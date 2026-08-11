package fsutil

import "os"

// hasExecutableMode는 Windows에서 항상 true입니다.
//
// Windows 파일 시스템에는 실행 비트가 없고, Go는 일반 파일의 mode를 0666으로
// 보고합니다. 실행 가능 여부는 확장자와 PATHEXT로 결정됩니다.
// upi-forge의 실제 실행 대상은 Linux bastion이며, Windows에서는 빌드와
// 테스트만 수행하므로 일반 파일이면 실행 가능한 것으로 봅니다.
func hasExecutableMode(os.FileMode) bool { return true }
