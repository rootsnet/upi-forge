package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"upi-forge/internal/config"
	"upi-forge/internal/logx"
	"upi-forge/internal/prompt"
)

// 병렬 실행은 노드마다 이 바이너리 자신을 하위 프로세스로 실행하는
// 방식입니다. 검증이 끝난 BMC 제어 코드(internal/idrac, internal/redfish)와
// 전역 logx를 전혀 수정하지 않고도 노드별 출력이 프로세스 단위로 깨끗하게
// 분리되기 때문입니다. 하위 프로세스는 NODE 인자를 하나만 받으므로 대상이
// 하나뿐이라 다시 병렬화하지 않고 순차 경로로 실행됩니다(재귀 없음).
//
// 비밀번호는 부모가 미리 입력받아 하위 프로세스의 환경 변수로만
// 전달합니다(childEnv 참고). 명령행과 로그에는 남지 않지만, 같은 OS
// 계정이나 root는 프로세스 환경을 조회할 수 있습니다.

// timestampFunc는 로그 디렉터리 이름의 실행 시각입니다. 테스트 고정용 변수입니다.
var timestampFunc = time.Now

// launchWorker는 노드 하나를 처리할 하위 프로세스를 실행하고 끝날 때까지
// 기다리며, 출력 전체를 logPath 파일에 남깁니다. 테스트에서는 실제
// 프로세스 대신 호출 기록을 남기는 구현으로 교체할 수 있도록 변수로 둡니다.
var launchWorker = func(ctx context.Context, argv []string, env []string, logPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("실행 파일 경로를 얻지 못했습니다: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("노드 로그 파일을 만들지 못했습니다: %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = env
	// 취소(Ctrl+C) 시 기본 동작은 즉시 강제 종료라 하위 프로세스의
	// defer(Redfish 세션 정리 등)가 실행되지 못합니다. 먼저 인터럽트를
	// 보내 스스로 정리하고 끝낼 기회를 주고, WaitDelay 안에 끝나지
	// 않으면 그때 강제 종료합니다. 인터럽트 전달을 지원하지 않는
	// 플랫폼(Windows)에서는 이전처럼 즉시 강제 종료합니다.
	//
	// 유예는 redfish.Client.Logout의 세션 삭제 시간 상한(20초)보다
	// 길어야 느린 BMC에서도 정리가 강제 종료로 끊기지 않습니다.
	// Logout의 상한을 바꾸면 이 값도 함께 확인해야 합니다.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 30 * time.Second
	return cmd.Run()
}

// childEnv는 하위 프로세스에 넘길 환경입니다.
//
// 부모 환경에서 BMC 비밀번호 변수를 모두 제거한 뒤, 이 노드의 비밀번호만
// 다시 넣습니다 — 부모에 여러 노드의 비밀번호 변수가 설정되어 있어도
// 하위 프로세스가 다른 노드의 비밀번호까지 상속하지 않게 합니다.
//
// 같은 값을 노드별 변수와 공통 변수 두 이름으로 넣습니다. 하위 프로세스는
// pathset을 다시 읽으므로, bmc.samePassword가 true면 공통 변수만 확인하고
// (prompt.CommonBMCPassword) 아니면 노드별 변수를 먼저 확인하기 때문입니다
// (prompt.BMCPassword). 하위 프로세스는 대상이 노드 하나뿐이라 두 이름이
// 같은 값이어도 모순이 없습니다.
func childEnv(host, password string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, prompt.EnvCommonPassword) {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		prompt.EnvCommonPassword+"="+password,
		prompt.EnvKey(host)+"="+password)
}

// runParallelBMCNodes는 대상 노드를 bmc.parallel 개수만큼 동시에 처리합니다.
//
// 노드별 상세 출력은 pathset의 logs/<명령>-<실행시각>-<고유값>/<hostname>.log에
// 남고, 화면에는 전체 진행(시작·완료·실패)만 표시합니다. 한 노드가 실패해도
// 나머지는 끝까지 진행하고, 마지막에 실패 노드 목록과 그 노드만 다시
// 실행할 명령을 안내합니다. 각 노드의 작업은 서로 독립적이라 영향을
// 주지 않습니다.
func runParallelBMCNodes(ctx context.Context, app *App, ps *config.Pathset, commandArgs []string,
	targets []string, passwords map[string]string) error {

	logRoot := filepath.Join(ps.Dir(), "logs")
	if err := os.MkdirAll(logRoot, 0o755); err != nil {
		return fmt.Errorf("로그 디렉터리를 만들지 못했습니다: %s: %w", logRoot, err)
	}
	// 같은 초에 시작한 두 실행이 같은 디렉터리를 공유해 노드 로그를 서로
	// 덮어쓰지 않도록, 시각 뒤에 고유 접미사를 붙여 원자적으로 만듭니다.
	logDir, err := os.MkdirTemp(logRoot,
		commandArgs[0]+"-"+timestampFunc().Format("20060102-150405")+"-")
	if err != nil {
		return fmt.Errorf("로그 디렉터리를 만들지 못했습니다: %s: %w", logRoot, err)
	}

	logx.Blank()
	logx.Info("병렬 실행: 대상 %d개, 동시 최대 %d개", len(targets), ps.BMC.Parallel)
	logx.Info("노드별 로그: %s", logDir)
	logx.Blank()

	sem := make(chan struct{}, ps.BMC.Parallel)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex // started, done, failed와 화면 출력 보호
		started int
		done    int
		failed  = map[string]bool{}
	)
	total := len(targets)

	for _, host := range targets {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Ctrl+C 후에는 새 노드를 시작하지 않습니다. 이미 시작한
			// 하위 프로세스는 exec.CommandContext가 종료합니다.
			if ctx.Err() != nil {
				mu.Lock()
				done++
				failed[host] = true
				logx.Warn("[%d/%d] %s: 중단되어 시작하지 않았습니다", done, total, host)
				mu.Unlock()
				return
			}

			logPath := filepath.Join(logDir, host+".log")
			mu.Lock()
			started++
			logx.Info("[시작 %d/%d] %s", started, total, host)
			mu.Unlock()

			err := launchWorker(ctx, app.WorkerArgs(commandArgs, host),
				childEnv(host, passwords[host]), logPath)

			mu.Lock()
			done++
			if err != nil {
				failed[host] = true
				logx.Warn("[%d/%d] %s 실패 — 로그: %s", done, total, host, logPath)
			} else {
				logx.Info("[%d/%d] %s 완료", done, total, host)
			}
			mu.Unlock()
		}(host)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("병렬 실행이 중단되었습니다: %w", err)
	}
	if len(failed) == 0 {
		return nil
	}

	// 완료 순서는 뒤섞이므로 실패 목록은 nodes.csv 순서로 정리합니다.
	ordered := make([]string, 0, len(failed))
	for _, host := range targets {
		if failed[host] {
			ordered = append(ordered, host)
		}
	}
	logx.Blank()
	logx.Warn("실패한 노드 %d개: %s", len(ordered), strings.Join(ordered, ", "))
	fmt.Fprintf(hintWriter, "\n%s 아래 노드별 로그에서 원인을 확인한 뒤, 다음 명령으로 실패한 노드만 다시 실행하세요:\n", logDir)
	fmt.Fprintf(hintWriter, "  %s\n", app.RetryCommand(commandArgs, ordered))
	return fmt.Errorf("%d개 노드 처리에 실패했습니다", len(ordered))
}
