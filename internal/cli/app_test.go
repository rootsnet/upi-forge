package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/logx"
)

const testConfig = `
cluster:
  domain: "mycluster.example.com"
registry:
  pullSecret: "pull-secret.json"
webServer:
  url: "http://127.0.0.1:58080"
  path: "/iso"
pathsets:
  active: "demo"
logger:
  debug: false
`

func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upi-forge.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --debug 플래그는 설정 파일의 logger.debug 값과 관계없이 유지되어야 합니다.
func TestDebugFlagSurvivesConfigLoad(t *testing.T) {
	t.Cleanup(func() { logx.Configure(logx.Options{}) })

	app := &App{configPath: writeConfig(t), debugFlag: true}
	logx.Configure(logx.Options{Debug: true}) // Execute가 플래그를 보고 켠 상태

	if _, err := app.Config(); err != nil {
		t.Fatalf("Config 실패: %v", err)
	}
	if !logx.DebugEnabled() {
		t.Error("설정 로드 후에도 --debug가 유지되어야 합니다 (logger.debug=false가 플래그를 덮으면 안 됨)")
	}
}

func TestDebugOffWithoutFlag(t *testing.T) {
	t.Cleanup(func() { logx.Configure(logx.Options{}) })

	app := &App{configPath: writeConfig(t)}
	if _, err := app.Config(); err != nil {
		t.Fatalf("Config 실패: %v", err)
	}
	if logx.DebugEnabled() {
		t.Error("플래그도 설정도 없으면 debug는 꺼져 있어야 합니다")
	}
}

// 재개 안내 명령은 현재 선택된 --config와 --pathset을 포함해야 합니다.
func TestResumeCommandIncludesGlobalFlags(t *testing.T) {
	app := &App{configPath: "/opt/upi-forge/configs/upi-forge.yaml", pathsetName: "pathset-3"}
	got := app.ResumeCommand("boot", "worker2.other.example.com")
	want := "upi-forge --config /opt/upi-forge/configs/upi-forge.yaml --pathset pathset-3 boot --from worker2.other.example.com"
	if got != want {
		t.Errorf("ResumeCommand:\n got=%s\nwant=%s", got, want)
	}

	// 기본 설정 경로로 실행했다면 --config를 덧붙이지 않습니다.
	plain := &App{configPath: "configs/upi-forge.yaml"}
	got = plain.ResumeCommand("boot", "w1")
	if strings.Contains(got, "--config") {
		t.Errorf("기본 경로면 --config를 생략해야 합니다: %s", got)
	}
	if got != "upi-forge boot --from w1" {
		t.Errorf("ResumeCommand: %s", got)
	}
}

// `eject --address`는 UPI_FORGE_IDRAC_PASSWORD에서 비밀번호를 읽을 수 있어야
// 하며, 비대화형 환경에서도 iDRAC 연결 단계까지 진행해야 합니다.
func TestEjectAddressUsesPasswordEnv(t *testing.T) {
	cfgPath := writeConfig(t)
	ctx := context.Background()

	// 환경 변수가 있으면 비밀번호 단계를 통과하고,
	// 닫힌 포트에 대한 연결 오류까지 도달해야 합니다.
	t.Setenv("UPI_FORGE_IDRAC_PASSWORD", "test-password")
	err := Execute(ctx, []string{"--config", cfgPath, "eject", "--address", "127.0.0.1:1"})
	if err == nil {
		t.Fatal("닫힌 포트에 대한 eject는 실패해야 합니다")
	}
	if strings.Contains(err.Error(), "비밀번호") || strings.Contains(err.Error(), "터미널") {
		t.Errorf("환경 변수가 있으면 비밀번호 단계에서 멈추면 안 됩니다: %v", err)
	}
	if !strings.Contains(err.Error(), "세션 생성 요청을 전송하지 못했습니다") {
		t.Errorf("iDRAC 연결 단계까지 진행되어야 합니다: %v", err)
	}
}

// TestEjectAddressWithoutEnvFailsSafely는 환경 변수도 터미널도 없으면
// 비밀번호를 읽지 않고 명확히 중단하는지 확인합니다(에코 노출 방지 규칙).
func TestEjectAddressWithoutEnvFailsSafely(t *testing.T) {
	cfgPath := writeConfig(t)

	// 테스트 프로세스의 stdin은 터미널이 아니므로 숨김 입력이 불가능합니다.
	err := Execute(context.Background(), []string{"--config", cfgPath, "eject", "--address", "127.0.0.1:1"})
	if err == nil {
		t.Fatal("비밀번호를 얻을 방법이 없으면 실패해야 합니다")
	}
	if !strings.Contains(err.Error(), "UPI_FORGE_IDRAC_PASSWORD") {
		t.Errorf("환경 변수 사용 안내가 있어야 합니다: %v", err)
	}
}
