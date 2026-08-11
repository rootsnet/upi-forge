// Package prompt는 BMC 비밀번호를 화면에 표시하지 않고 입력받습니다.
//
// 비밀번호는 CSV나 설정 파일에 저장하지 않고, 프로세스 목록에 노출되지 않도록
// 명령행 인자로도 전달하지 않습니다.
//
// 대화형 입력이 어려운 자동화 환경에서는 환경 변수를 사용할 수 있습니다.
// 노드별 변수를 먼저 확인하고, 없으면 공통 변수를 사용합니다.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnvCommonPassword는 모든 노드에 같은 BMC 비밀번호를 쓸 때 사용하는 환경
// 변수입니다. 변수 이름의 IDRAC 표기는 운영 호환을 위해 유지합니다.
const EnvCommonPassword = "UPI_FORGE_IDRAC_PASSWORD"

// EnvPasswordPrefix + <정규화한 hostname> 형태로 노드별 비밀번호를 지정할 수 있습니다.
// 예: worker4.mycluster.example.com -> UPI_FORGE_IDRAC_PASSWORD_WORKER4_MYCLUSTER_EXAMPLE_COM
const EnvPasswordPrefix = "UPI_FORGE_IDRAC_PASSWORD_"

// ErrEmptyPassword는 비밀번호가 비어 있을 때 반환합니다.
var ErrEmptyPassword = errors.New("비밀번호가 비어 있습니다")

// EnvKey는 hostname을 환경 변수 이름으로 정규화합니다.
func EnvKey(hostname string) string {
	var b strings.Builder
	b.WriteString(EnvPasswordPrefix)
	for _, r := range strings.ToUpper(hostname) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// BMCPassword는 노드의 BMC 비밀번호를 얻습니다.
// 환경 변수가 있으면 그 값을 쓰고, 없으면 터미널에서 숨김 입력을 받습니다.
func BMCPassword(hostname string) (string, error) {
	if v, ok := os.LookupEnv(EnvKey(hostname)); ok {
		if v == "" {
			return "", fmt.Errorf("%w (%s)", ErrEmptyPassword, EnvKey(hostname))
		}
		return v, nil
	}
	if v, ok := os.LookupEnv(EnvCommonPassword); ok {
		if v == "" {
			return "", fmt.Errorf("%w (%s)", ErrEmptyPassword, EnvCommonPassword)
		}
		return v, nil
	}
	return Secret("BMC 비밀번호 입력: ")
}

// Secret은 에코 없이 한 줄을 입력받습니다.
func Secret(label string) (string, error) {
	fd := os.Stdin.Fd()
	if !isTerminal(fd) {
		// 터미널이 아니면(파이프, CI 등) 숨김 입력을 할 수 없습니다.
		// 비밀번호가 화면이나 로그에 남는 것을 막기 위해 여기서 명확히 중단합니다.
		return "", fmt.Errorf("표준 입력이 터미널이 아니라 비밀번호를 숨겨 입력할 수 없습니다. "+
			"%s 또는 %s<HOSTNAME> 환경 변수를 사용하세요", EnvCommonPassword, EnvPasswordPrefix)
	}
	fmt.Fprint(os.Stderr, label)
	secret, err := readSecret(fd, os.Stdin)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("비밀번호 입력을 완료하지 못했습니다: %w", err)
	}
	if secret == "" {
		return "", ErrEmptyPassword
	}
	return secret, nil
}

// Confirm은 y/N 확인을 받습니다. 터미널이 아니면 기본값 false로 처리합니다.
func Confirm(label string) bool {
	if !isTerminal(os.Stdin.Fd()) {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
