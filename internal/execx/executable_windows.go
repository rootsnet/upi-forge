package execx

import "os"

// isExecutableFile은 Windows에서 일반 파일이면 실행 가능한 것으로 봅니다.
//
// Windows에는 실행 비트가 없어 Go가 일반 파일의 mode를 0666으로 보고합니다.
// 실행 가능 여부는 확장자와 PATHEXT가 결정하므로, 경로를 직접 지정한 경우에는
// 일반 파일인지만 확인하고 실제 실행 가능 여부는 exec에 맡깁니다.
func isExecutableFile(info os.FileInfo) bool { return info.Mode().IsRegular() }
