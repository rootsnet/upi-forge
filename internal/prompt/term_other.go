//go:build !linux

package prompt

import (
	"fmt"
	"os"
)

// isTerminal은 지원하지 않는 플랫폼에서 항상 false를 반환합니다.
// 그 결과 Secret이 환경 변수 사용을 안내하며 중단하므로,
// 비밀번호가 화면에 그대로 보이는 상황은 발생하지 않습니다.
func isTerminal(uintptr) bool { return false }

func readSecret(uintptr, *os.File) (string, error) {
	return "", fmt.Errorf("이 플랫폼에서는 숨김 입력을 지원하지 않습니다")
}
