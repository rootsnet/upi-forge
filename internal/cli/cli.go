// Package cli는 이미지 준비, Ignition 및 ISO 생성, iDRAC 부팅을 위한
// upi-forge 서브커맨드를 제공합니다. 단절망에서도 추가 모듈 없이 빌드할 수
// 있도록 명령행 해석에는 표준 라이브러리 flag를 사용합니다.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"upi-forge/internal/config"
	"upi-forge/internal/logx"
)

// Version은 빌드 시 -ldflags로 주입합니다.
var Version = "dev"

// command는 서브커맨드 하나입니다.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, app *App, args []string) error
}

// App은 서브커맨드가 공유하는 실행 상태입니다.
type App struct {
	configPath  string
	pathsetName string // --pathset으로 덮어쓴 이름. 비면 설정의 active를 씁니다.
	debugFlag   bool   // --debug 플래그. 설정 파일 값보다 우선합니다.

	cfg *config.Config
}

// Config는 설정을 한 번만 읽어 재사용합니다.
func (a *App) Config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return nil, err
	}
	// 명령행의 --debug는 설정 파일의 logger.debug와 합쳐 적용합니다.
	logx.Configure(logx.Options{
		Debug:     cfg.Logger.Debug || a.debugFlag,
		Timestamp: cfg.Logger.Timestamp,
	})
	a.cfg = cfg
	return cfg, nil
}

// ResumeCommand는 실패한 노드부터 이어서 실행할 명령 문자열을 만듭니다.
// 사용자가 --config나 --pathset을 지정해 실행했다면 그대로 포함해야
// 안내된 명령을 복사해 실행했을 때 같은 설정으로 재개됩니다.
// commandName에는 사용자가 지정한 서브커맨드 플래그도 포함될 수 있습니다
// (예: "boot --skip-iso-check") — 안내된 명령이 원래 실행과 같은 옵션으로
// 재개되어야 하기 때문입니다.
func (a *App) ResumeCommand(commandName, host string) string {
	parts := []string{"upi-forge"}
	if a.configPath != "" && a.configPath != config.DefaultPath {
		parts = append(parts, "--config", a.configPath)
	}
	if a.pathsetName != "" {
		parts = append(parts, "--pathset", a.pathsetName)
	}
	parts = append(parts, commandName, "--from", host)
	return strings.Join(parts, " ")
}

// Pathset은 선택된 하드웨어 그룹 설정을 읽습니다.
func (a *App) Pathset() (*config.Config, *config.Pathset, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, nil, err
	}
	name := a.pathsetName
	if name == "" {
		name = cfg.Pathsets.Active
	}
	ps, err := cfg.LoadPathsetNamed(name)
	if err != nil {
		return nil, nil, err
	}
	return cfg, ps, nil
}

var commands = []command{
	{"prepare", "RHCOS ISO, coreos-installer, minimal ISO와 rootfs를 준비합니다", runPrepare},
	{"ignition", "선택한 pathset의 노드별 NMState YAML과 Ignition을 생성합니다", runIgnition},
	{"iso", "선택한 pathset의 노드별 설치 ISO를 생성합니다", runISO},
	{"boot", "노드별 설치 ISO를 iDRAC Virtual Media로 부팅합니다", runBoot},
	{"live-boot", "공용 CoreOS ISO로 라이브 부팅해 하드웨어를 조사합니다", runLiveBoot},
	{"inventory", "iDRAC에서 NIC와 스토리지 인벤토리를 수집합니다", runInventory},
	{"eject", "iDRAC에 연결된 Virtual Media를 모두 제거합니다", runEject},
	{"config", "읽어 들인 설정과 계산된 값을 확인합니다", runConfigShow},
	{"version", "버전을 출력합니다", runVersion},
}

// Execute는 명령행 인자를 해석해 서브커맨드를 실행합니다.
func Execute(ctx context.Context, args []string) error {
	app := &App{}
	fs := flag.NewFlagSet("upi-forge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&app.configPath, "config", envOr("UPI_FORGE_CONFIG", config.DefaultPath),
		"설정 파일 경로")
	fs.StringVar(&app.pathsetName, "pathset", "",
		"설정의 pathsets.active 대신 사용할 pathset 이름")
	fs.BoolVar(&app.debugFlag, "debug", false, "상세 로그를 출력합니다")
	fs.Usage = func() { printUsage(fs) }

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if app.debugFlag {
		logx.Configure(logx.Options{Debug: true})
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(fs)
		return fmt.Errorf("실행할 명령을 지정하세요")
	}

	name := rest[0]
	for _, cmd := range commands {
		if cmd.name != name {
			continue
		}
		if err := cmd.run(ctx, app, rest[1:]); err != nil {
			return err
		}
		return nil
	}
	printUsage(fs)
	return fmt.Errorf("알 수 없는 명령입니다: %s", name)
}

func printUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, `upi-forge - OpenShift UPI 노드 추가 자동화 (%s)

사용법:
  upi-forge [전역 옵션] <명령> [명령 옵션]

명령:
`, Version)

	names := make([]string, 0, len(commands))
	width := 0
	for _, cmd := range commands {
		names = append(names, cmd.name)
		if len(cmd.name) > width {
			width = len(cmd.name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, cmd := range commands {
			if cmd.name == name {
				fmt.Fprintf(out, "  %-*s  %s\n", width, cmd.name, cmd.summary)
			}
		}
	}

	fmt.Fprintf(out, "\n전역 옵션:\n")
	fs.PrintDefaults()
	fmt.Fprintf(out, `
작업 순서:
  1) upi-forge prepare
  2) upi-forge ignition
  3) upi-forge iso
  4) 생성된 ISO를 웹 서버에 게시
  5) upi-forge boot

각 명령의 옵션은 다음으로 확인합니다.
  upi-forge <명령> -h
`)
}

// newFlagSet은 서브커맨드용 FlagSet을 만듭니다.
func newFlagSet(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s\n\n옵션:\n", strings.TrimRight(usage, "\n"))
		fs.PrintDefaults()
	}
	return fs
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func runVersion(_ context.Context, _ *App, _ []string) error {
	fmt.Println("upi-forge " + Version)
	return nil
}
