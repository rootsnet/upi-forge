package cli

// bmc.parallel의 병렬 실행 흐름을 하위 프로세스 없이 고정합니다.
// launchWorker를 가짜로 바꿔 "어떤 노드가 어떤 인자·환경으로 실행되는지",
// "동시 실행이 한도를 넘지 않는지", "일부 실패 시 나머지가 끝까지 도는지"를
// 검증합니다. 실제 하위 프로세스 실행 자체는 이 바이너리의 일반 순차 경로라
// 기존 디스패치 테스트가 커버합니다.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/logx"
	"upi-forge/internal/prompt"
	"upi-forge/internal/redfish"
)

// fakeWorkerRun은 가짜 launchWorker가 기록한 호출 하나입니다.
type fakeWorkerRun struct {
	argv    []string
	env     []string
	logPath string
}

// installFakeWorker는 launchWorker를 기록용 가짜로 바꿉니다.
// failHosts에 있는 노드는 실패를 흉내 냅니다.
func installFakeWorker(t *testing.T, failHosts ...string) *struct {
	sync.Mutex
	runs          []fakeWorkerRun
	running, peak int
} {
	t.Helper()
	state := &struct {
		sync.Mutex
		runs          []fakeWorkerRun
		running, peak int
	}{}

	orig := launchWorker
	t.Cleanup(func() { launchWorker = orig })
	launchWorker = func(ctx context.Context, argv []string, env []string, logPath string) error {
		state.Lock()
		state.runs = append(state.runs, fakeWorkerRun{argv: argv, env: env, logPath: logPath})
		state.running++
		if state.running > state.peak {
			state.peak = state.running
		}
		host := argv[len(argv)-1]
		state.Unlock()

		// 실행이 즉시 끝나면 동시 실행이 겹치지 않아 peak 측정이 무의미해
		// 집니다. 짧게 대기해 한도가 없을 때는 반드시 겹치게 합니다.
		// (한도 준수 자체는 세마포어가 시간과 무관하게 보장하므로 이 대기가
		// 한도 검사를 불안정하게 만들지는 않습니다.)
		time.Sleep(20 * time.Millisecond)
		defer func() {
			state.Lock()
			state.running--
			state.Unlock()
		}()
		if slices.Contains(failHosts, host) {
			return fmt.Errorf("가짜 실패")
		}
		return nil
	}
	return state
}

func TestParallelBootRunsEveryNodeAndSummarizesFailures(t *testing.T) {
	configPath := bmcCommandEnvWith(t, "bmc:\n  parallel: 2\n  samePassword: true\n",
		"worker1", "worker2", "worker3")
	installFakeBMC(t) // 공통 비밀번호 환경 변수 설정 포함
	// 부모 환경에 남아 있을 수 있는 다른 노드의 비밀번호가 하위 프로세스로
	// 상속되지 않는지 확인하기 위한 값입니다.
	t.Setenv(prompt.EnvPasswordPrefix+"OTHERHOST", "other-secret")
	workers := installFakeWorker(t, "worker2")

	err := runBMCCommand(t, configPath, "boot")
	if err == nil {
		t.Fatal("실패한 노드가 있으면 오류를 반환해야 합니다")
	}
	if !strings.Contains(err.Error(), "1개 노드") {
		t.Errorf("실패 개수가 요약되어야 합니다: %v", err)
	}

	// 한 노드가 실패해도 세 노드 모두 시도되어야 합니다.
	if len(workers.runs) != 3 {
		t.Fatalf("모든 노드가 실행되어야 합니다: %d", len(workers.runs))
	}
	seen := map[string]fakeWorkerRun{}
	for _, run := range workers.runs {
		seen[run.argv[len(run.argv)-1]] = run
	}
	for _, host := range []string{"worker1", "worker2", "worker3"} {
		run, ok := seen[host]
		if !ok {
			t.Errorf("%s가 실행되어야 합니다", host)
			continue
		}
		// 하위 프로세스는 전역 옵션과 명령, NODE 인자 하나를 받아야 합니다.
		// pathset은 --pathset 없이 active로 선택한 실행이라도 해석된
		// 이름으로 명시되어야 합니다 — 실행 도중 active가 바뀌어도 하위
		// 프로세스가 같은 pathset(같은 장비)을 대상으로 하기 위해서입니다.
		want := []string{"--config", configPath, "--pathset", "demo", "boot", host}
		if !slices.Equal(run.argv, want) {
			t.Errorf("%s 인자:\n got=%v\nwant=%v", host, run.argv, want)
		}
		// 비밀번호는 명령행이 아니라 환경 변수로 전달되어야 합니다.
		// 하위 프로세스가 pathset을 다시 읽어 samePassword 여부에 따라
		// 다른 변수를 찾으므로(공통: CommonBMCPassword, 노드별: BMCPassword)
		// 두 이름이 모두 있어야 합니다. 공통 변수가 빠지면 samePassword
		// 병렬의 대화형 입력 경로가 실기에서 실패합니다.
		if !slices.Contains(run.env, prompt.EnvKey(host)+"=test-password") {
			t.Errorf("%s: 노드별 비밀번호 환경 변수가 전달되어야 합니다", host)
		}
		if !slices.Contains(run.env, prompt.EnvCommonPassword+"=test-password") {
			t.Errorf("%s: 공통 비밀번호 환경 변수가 전달되어야 합니다", host)
		}
		// 다른 노드의 비밀번호는 상속하면 안 됩니다(부모 환경 정리 확인).
		for _, kv := range run.env {
			if strings.HasPrefix(kv, prompt.EnvPasswordPrefix) &&
				!strings.HasPrefix(kv, prompt.EnvKey(host)+"=") {
				t.Errorf("%s: 다른 노드의 비밀번호 변수가 상속되면 안 됩니다: %s",
					host, strings.SplitN(kv, "=", 2)[0])
			}
		}
		// 노드별 로그는 pathset의 logs/ 아래 <hostname>.log여야 합니다.
		wantDir := filepath.Join(filepath.Dir(configPath), "pathsets", "demo", "logs")
		if !strings.HasPrefix(run.logPath, wantDir) || filepath.Base(run.logPath) != host+".log" {
			t.Errorf("%s 로그 경로가 pathset의 logs/ 아래여야 합니다: %s", host, run.logPath)
		}
		if _, statErr := os.Stat(filepath.Dir(run.logPath)); statErr != nil {
			t.Errorf("로그 디렉터리가 만들어져야 합니다: %v", statErr)
		}
	}
}

func TestParallelRespectsConcurrencyLimit(t *testing.T) {
	hosts := []string{"w1", "w2", "w3", "w4", "w5"}
	configPath := bmcCommandEnvWith(t, "bmc:\n  parallel: 2\n  samePassword: true\n", hosts...)
	installFakeBMC(t)
	workers := installFakeWorker(t)

	var out bytes.Buffer
	logx.SetOutput(&out, &out)
	t.Cleanup(func() { logx.SetOutput(os.Stdout, os.Stderr) })
	if err := Execute(context.Background(),
		[]string{"--config", configPath, "eject"}); err != nil {
		t.Fatalf("eject 실패: %v\n출력:\n%s", err, out.String())
	}
	if len(workers.runs) != len(hosts) {
		t.Fatalf("모든 노드가 실행되어야 합니다: %d", len(workers.runs))
	}
	if workers.peak > 2 {
		t.Errorf("동시 실행이 bmc.parallel을 넘으면 안 됩니다: peak=%d", workers.peak)
	}
	// 시작 진행 번호는 1..N이 한 번씩이어야 합니다. 완료 수 기반으로 매기면
	// 완료 전에 시작한 작업들이 같은 번호를 받습니다.
	starts := regexp.MustCompile(`\[시작 (\d+)/`).FindAllStringSubmatch(out.String(), -1)
	if len(starts) != len(hosts) {
		t.Fatalf("시작 표시가 노드 수만큼 있어야 합니다: %d\n출력:\n%s", len(starts), out.String())
	}
	seenNo := map[string]bool{}
	for _, m := range starts {
		if seenNo[m[1]] {
			t.Errorf("시작 번호가 중복되면 안 됩니다: %s\n출력:\n%s", m[1], out.String())
		}
		seenNo[m[1]] = true
	}
}

// 같은 초에 시작한 두 실행이 로그 디렉터리를 공유하면 안 됩니다.
func TestParallelLogDirUniquePerRun(t *testing.T) {
	configPath := bmcCommandEnvWith(t, "bmc:\n  parallel: 2\n  samePassword: true\n", "w1", "w2")
	installFakeBMC(t)
	workers := installFakeWorker(t)

	// 두 실행의 시각을 같게 고정해 같은 초 시나리오를 재현합니다.
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	orig := timestampFunc
	t.Cleanup(func() { timestampFunc = orig })
	timestampFunc = func() time.Time { return fixed }

	for i := 0; i < 2; i++ {
		if err := runBMCCommand(t, configPath, "eject"); err != nil {
			t.Fatalf("eject %d회차 실패: %v", i+1, err)
		}
	}
	dirs := map[string]bool{}
	for _, run := range workers.runs {
		dirs[filepath.Dir(run.logPath)] = true
	}
	if len(dirs) != 2 {
		t.Errorf("실행마다 로그 디렉터리가 달라야 합니다: %v", dirs)
	}
}

// 대상이 하나면 bmc.parallel이 켜져 있어도 하위 프로세스 없이 순차 경로로
// 실행되어야 합니다. 병렬 실행의 하위 프로세스가 NODE 인자 하나로 실행될 때
// 다시 병렬화(재귀)하지 않는 것도 같은 규칙이 보장합니다.
func TestParallelSingleTargetRunsSequentially(t *testing.T) {
	configPath := bmcCommandEnvWith(t, "bmc:\n  parallel: 4\n", "worker1", "worker2")
	calls := installFakeBMC(t)
	workers := installFakeWorker(t)

	if err := runBMCCommand(t, configPath, "boot", "worker2"); err != nil {
		t.Fatalf("boot NODE 실패: %v", err)
	}
	if len(workers.runs) != 0 {
		t.Errorf("대상이 하나면 하위 프로세스를 쓰지 않아야 합니다: %d", len(workers.runs))
	}
	if calls.boot != 1 {
		t.Errorf("순차 경로로 Boot이 1회 호출되어야 합니다: %d", calls.boot)
	}
}

// NODE 인자는 지정한 노드만 처리해야 하고, --from과는 함께 쓸 수 없습니다.
func TestNodeArgumentsSelectTargets(t *testing.T) {
	configPath := bmcCommandEnvWith(t, "", "worker1", "worker2", "worker3")
	calls := installFakeBMC(t)

	if err := runBMCCommand(t, configPath, "eject", "worker3", "worker1"); err != nil {
		t.Fatalf("eject NODE 실패: %v", err)
	}
	if calls.eject != 2 {
		t.Errorf("지정한 두 노드만 처리해야 합니다: %d", calls.eject)
	}

	err := runBMCCommand(t, configPath, "eject", "--from", "worker2", "worker3")
	if err == nil || !strings.Contains(err.Error(), "--from과 NODE") {
		t.Errorf("--from과 NODE 인자를 함께 쓰면 오류여야 합니다: %v", err)
	}

	err = runBMCCommand(t, configPath, "eject", "no-such-node")
	if err == nil {
		t.Error("CSV에 없는 노드는 오류여야 합니다")
	}
}

func TestRetryCommandListsFailedNodes(t *testing.T) {
	app := &App{configPath: "/tmp/upi-forge.yaml", pathsetName: "ps3"}
	got := app.RetryCommand([]string{"boot", "--skip-iso-check"}, []string{"w2", "w5"})
	want := "upi-forge --config /tmp/upi-forge.yaml --pathset ps3 boot --skip-iso-check w2 w5"
	if got != want {
		t.Errorf("RetryCommand:\n got=%s\nwant=%s", got, want)
	}

	// --pathset 없이 active로 선택한 실행도, 해석된 이름이 명시되어야
	// 합니다(그 사이 active가 바뀌어도 같은 pathset을 대상으로).
	active := &App{configPath: "/tmp/upi-forge.yaml", resolvedPathset: "ps7"}
	got = active.RetryCommand([]string{"eject"}, []string{"w1"})
	want = "upi-forge --config /tmp/upi-forge.yaml --pathset ps7 eject w1"
	if got != want {
		t.Errorf("RetryCommand(active):\n got=%s\nwant=%s", got, want)
	}
}

func TestWorkerArgsCarryGlobalAndCommandFlags(t *testing.T) {
	app := &App{configPath: "/tmp/upi-forge.yaml", debugFlag: true}
	got := app.WorkerArgs([]string{"live-boot", "--skip-iso-check"}, "w1")
	want := []string{"--config", "/tmp/upi-forge.yaml", "--debug", "live-boot", "--skip-iso-check", "w1"}
	if !slices.Equal(got, want) {
		t.Errorf("WorkerArgs:\n got=%v\nwant=%v", got, want)
	}
}

// NODE 인자로 대상을 지정한 순차 실행이 실패하면, --from(파일 순서 기준 —
// 지정하지 않은 노드까지 대상이 넓어짐)이 아니라 남은 노드 목록 그대로
// 다시 실행하도록 안내해야 합니다.
func TestSequentialNodeSelectedFailureHintsRetryWithNodes(t *testing.T) {
	configPath := bmcCommandEnvWith(t, "", "worker1", "worker2", "worker3")
	installFakeBMC(t)

	// worker2에서 실패하는 세션으로 바꿉니다.
	origSession := withSession
	t.Cleanup(func() { withSession = origSession })
	withSession = func(ctx context.Context, cfg *config.Config, target csvdata.IDRAC,
		password string, fn func(client *redfish.Client) error) error {
		if target.Hostname == "worker2" {
			return fmt.Errorf("가짜 세션 실패")
		}
		return fn(nil)
	}

	var hints bytes.Buffer
	origWriter := hintWriter
	t.Cleanup(func() { hintWriter = origWriter })
	hintWriter = &hints

	// NODE 지정 실행: 실패한 worker2부터 남은 지정 노드만 안내해야 합니다.
	err := runBMCCommand(t, configPath, "eject", "worker1", "worker2", "worker3")
	if err == nil {
		t.Fatal("worker2 실패가 오류로 보고되어야 합니다")
	}
	if want := "eject worker2 worker3"; !strings.Contains(hints.String(), want) {
		t.Errorf("NODE 지정 실행의 안내에 남은 노드 목록이 있어야 합니다: want %q\n안내:\n%s",
			want, hints.String())
	}
	if strings.Contains(hints.String(), "--from") {
		t.Errorf("NODE 지정 실행의 안내에 --from이 있으면 안 됩니다:\n%s", hints.String())
	}
	// active로 선택한 pathset도 안내 명령에는 명시되어야 합니다.
	if !strings.Contains(hints.String(), "--pathset demo") {
		t.Errorf("안내 명령에 해석된 pathset이 명시되어야 합니다:\n%s", hints.String())
	}

	// 파일 순서 실행: 기존처럼 --from 재개를 안내해야 합니다.
	hints.Reset()
	if err := runBMCCommand(t, configPath, "eject"); err == nil {
		t.Fatal("worker2 실패가 오류로 보고되어야 합니다")
	}
	if want := "eject --from worker2"; !strings.Contains(hints.String(), want) {
		t.Errorf("파일 순서 실행의 안내는 --from 재개여야 합니다: want %q\n안내:\n%s",
			want, hints.String())
	}
}
