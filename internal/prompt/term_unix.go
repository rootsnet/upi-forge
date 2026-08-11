// TCGETS/TCSETS는 Linux 전용 상수입니다(darwin은 TIOCGETA/TIOCSETA라
// 이 파일이 컴파일되지 않습니다). 실행 대상이 Linux bastion뿐이므로
// darwin은 term_other.go의 폴백(환경 변수 안내)을 사용합니다.
//go:build linux

package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// termios 관련 ioctl 요청 번호입니다.
// golang.org/x/term에 의존하지 않기 위해 직접 호출합니다.
// (단절망 bastion에서 외부 모듈 없이 빌드할 수 있어야 합니다.)
const (
	ioctlReadTermios  = syscall.TCGETS
	ioctlWriteTermios = syscall.TCSETS
)

// isTerminal은 파일 디스크립터가 터미널인지 확인합니다.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}

// readSecret은 에코를 끈 상태로 한 줄을 읽습니다.
func readSecret(fd uintptr, in *os.File) (string, error) {
	var original syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&original)), 0, 0, 0); errno != 0 {
		return "", fmt.Errorf("터미널 설정을 읽지 못했습니다: %w", errno)
	}

	noEcho := original
	noEcho.Lflag &^= syscall.ECHO
	noEcho.Lflag |= syscall.ICANON | syscall.ISIG
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlWriteTermios,
		uintptr(unsafe.Pointer(&noEcho)), 0, 0, 0); errno != 0 {
		return "", fmt.Errorf("터미널 에코를 끄지 못했습니다: %w", errno)
	}
	// 어떤 경로로 끝나든 터미널 설정을 반드시 되돌립니다.
	defer func() {
		_, _, _ = syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlWriteTermios,
			uintptr(unsafe.Pointer(&original)), 0, 0, 0)
	}()

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
