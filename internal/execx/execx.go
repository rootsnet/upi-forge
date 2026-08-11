// Package execx는 외부 명령 실행을 한곳으로 모읍니다.
//
// upi-forge는 OpenShift 계열 도구(oc, butane, coreos-installer)만 외부 명령으로
// 실행합니다. 이 도구들은 Go로 대체할 라이브러리가 없거나, 있더라도 클러스터
// 버전과 정확히 맞춰야 해서 바이너리를 그대로 쓰는 편이 안전합니다.
//
// 그 밖의 jq, openssl, base64, awk, sed, curl은 모두 Go 표준 라이브러리로
// 대체했으므로 실행 호스트에 설치되어 있지 않아도 됩니다.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"upi-forge/internal/logx"
)

// ErrNotFound는 필요한 외부 명령을 PATH에서 찾지 못했을 때 반환합니다.
var ErrNotFound = errors.New("필수 명령을 찾을 수 없습니다")

// Require는 명령이 실행 가능한지 확인하고 실제 경로를 반환합니다.
func Require(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		info, err := os.Stat(name)
		if err != nil || !isExecutableFile(info) {
			return "", fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return name, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return path, nil
}

// Output은 명령을 실행하고 표준 출력을 문자열로 반환합니다.
// 실패하면 표준 오류 내용을 오류 메시지에 포함합니다.
func Output(ctx context.Context, name string, args ...string) (string, error) {
	logx.Debug("실행: %s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", wrap(name, args, stderr.String(), err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// Run은 명령을 실행하고 표준 출력과 표준 오류를 그대로 화면에 흘려보냅니다.
// coreos-installer처럼 진행 상황을 사람이 봐야 하는 명령에 사용합니다.
func Run(ctx context.Context, name string, args ...string) error {
	logx.Debug("실행: %s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return wrap(name, args, "", err)
	}
	return nil
}

// RunTo는 명령의 표준 출력을 파일로 저장하고 표준 오류는 화면에 보냅니다.
// butane 렌더 결과를 파일로 받을 때 사용합니다.
func RunTo(ctx context.Context, dest *bytes.Buffer, name string, args ...string) error {
	logx.Debug("실행: %s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stdout = dest
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return wrap(name, args, stderr.String(), err)
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		logx.Debug("%s stderr: %s", name, s)
	}
	return nil
}

func wrap(name string, args []string, stderr string, err error) error {
	cmdline := name
	if len(args) > 0 {
		cmdline += " " + strings.Join(args, " ")
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("명령 실행에 실패했습니다: %s: %w\n%s", cmdline, err, s)
	}
	return fmt.Errorf("명령 실행에 실패했습니다: %s: %w", cmdline, err)
}
