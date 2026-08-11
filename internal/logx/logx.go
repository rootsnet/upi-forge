// Package logx는 upi-forge 전체가 공유하는 CLI 로거입니다. 운영자가 항상
// 확인해야 하는 진행 메시지와 선택적으로 표시하는 디버그 메시지를 구분합니다.
//
//	Step/Info/Warn : 운영자가 항상 봐야 하는 진행 상황. 표준 출력으로 나갑니다.
//	Debug          : 설정에서 켰을 때만 나오는 상세 로그. 표준 오류로 나갑니다.
//
// 오류는 error 값으로 호출자에게 전달하고 cmd 계층에서 한 번만 출력합니다.
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	mu           sync.Mutex
	debugEnabled bool
	showTime     bool
	out          io.Writer = os.Stdout
	errOut       io.Writer = os.Stderr
	stepCounter  int
	stepTotal    int
)

// Options는 설정 파일에서 읽은 로거 동작을 담습니다.
type Options struct {
	Debug     bool
	Timestamp bool
}

// Configure는 설정 파일 값을 로거에 반영합니다.
func Configure(o Options) {
	mu.Lock()
	defer mu.Unlock()
	debugEnabled = o.Debug
	showTime = o.Timestamp
}

// SetOutput은 테스트에서 출력 대상을 바꾸기 위해 사용합니다.
func SetOutput(stdout, stderr io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out, errOut = stdout, stderr
}

// DebugEnabled는 비용이 큰 로그 문자열을 만들기 전에 확인할 때 사용합니다.
func DebugEnabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return debugEnabled
}

func prefix() string {
	if !showTime {
		return ""
	}
	return time.Now().Format("2006-01-02T15:04:05Z07:00") + " "
}

func write(w io.Writer, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	msg := sanitize(fmt.Sprintf(format, args...))
	fmt.Fprintf(w, "%s%s\n", prefix(), msg)
}

// sanitize는 출력에서 제어 문자를 U+FFFD로 바꿉니다(개행·탭은 유지).
// BMC 응답 본문, DNS PTR 이름 등 서버가 제어하는 문자열이 로그에 그대로
// 실리므로, ESC 시퀀스로 운영자 터미널의 이전 줄을 지우거나 가짜 진행
// 메시지를 그리는 것을 막습니다.
func sanitize(s string) string {
	clean := true
	for _, r := range s {
		if isBadRune(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isBadRune(r) {
			return '�'
		}
		return r
	}, s)
}

func isBadRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f
}

// Info는 일반 진행 정보를 출력합니다.
func Info(format string, args ...any) { write(out, format, args...) }

// Warn은 실행을 중단하지 않는 경고를 출력합니다.
func Warn(format string, args ...any) { write(errOut, "경고: "+format, args...) }

// Debug는 설정에서 debug를 켰을 때만 출력합니다.
func Debug(format string, args ...any) {
	if !DebugEnabled() {
		return
	}
	write(errOut, "debug: "+format, args...)
}

// Blank는 사람이 읽기 좋게 빈 줄을 넣습니다.
func Blank() { write(out, "") }

// BeginSteps는 "[1/2] ..." 형태로 진행 단계를 표시하도록 준비합니다.
func BeginSteps(total int) {
	mu.Lock()
	defer mu.Unlock()
	stepCounter = 0
	stepTotal = total
}

// Step은 BeginSteps로 지정한 전체 단계 중 다음 단계를 출력합니다.
func Step(format string, args ...any) {
	mu.Lock()
	stepCounter++
	current, total := stepCounter, stepTotal
	mu.Unlock()

	msg := fmt.Sprintf(format, args...)
	if total > 0 {
		write(out, "[%d/%d] %s", current, total, msg)
		return
	}
	write(out, "== %s ==", msg)
}

// Section은 사람이 구분하기 쉬운 제목 줄을 출력합니다.
func Section(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	write(out, "\n== %s ==", msg)
}

// KeyValues는 실행 요약을 정렬된 형태로 출력합니다.
func KeyValues(pairs ...string) {
	if len(pairs)%2 != 0 {
		panic("logx.KeyValues는 짝수 개의 인자가 필요합니다")
	}
	width := 0
	for i := 0; i < len(pairs); i += 2 {
		if n := runeLen(pairs[i]); n > width {
			width = n
		}
	}
	for i := 0; i < len(pairs); i += 2 {
		pad := strings.Repeat(" ", width-runeLen(pairs[i]))
		Info("%s%s : %s", pairs[i], pad, pairs[i+1])
	}
}

// runeLen은 터미널에서 차지하는 열 수를 계산합니다.
// 한글과 CJK 문자는 두 칸을 차지하므로 단순 rune 개수로는 정렬이 맞지 않습니다.
func runeLen(s string) int {
	width := 0
	for _, r := range s {
		if isWide(r) {
			width += 2
			continue
		}
		width++
	}
	return width
}

// isWide는 East Asian Wide/Fullwidth 문자인지 판단합니다.
// 이 도구의 로그 레이블에 필요한 범위만 다룹니다.
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 한글 자모
		r >= 0x2E80 && r <= 0xA4CF, // CJK 부수, 한자, 가나
		r >= 0xAC00 && r <= 0xD7A3, // 한글 음절
		r >= 0xF900 && r <= 0xFAFF, // CJK 호환 한자
		r >= 0xFE30 && r <= 0xFE6F, // CJK 호환 형식
		r >= 0xFF00 && r <= 0xFF60, // 전각 형식
		r >= 0xFFE0 && r <= 0xFFE6:
		return true
	}
	return false
}
