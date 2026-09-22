package cli

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"

	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/logx"
	"upi-forge/internal/prompt"
	"upi-forge/internal/redfish"
)

const ejectUsage = `upi-forge eject - iDRAC Virtual Media를 제거합니다.

기존 셸 스크립트의 가상 미디어 제거 절차에 해당합니다.

설치가 끝난 뒤 마운트된 ISO를 떼어 내고, 모든 장치가 Inserted=false인지 확인합니다.

사용법:
  upi-forge eject [--from HOSTNAME] [NODE ...]
  upi-forge eject --address IDRAC_IP [--username root] [--bmc-type idrac10|idrac9]

--address를 지정하면 CSV와 무관하게 iDRAC 한 대만 처리합니다. 이때는
pathset의 bmc.type을 알 수 없으므로 --bmc-type으로 장비 종류를 지정합니다
(생략 시 idrac10. iDRAC9 장비는 가상 미디어 경로가 달라 반드시 idrac9로
지정해야 합니다). 지정하지 않으면 선택한 pathset의 idracs.csv 전체를
처리합니다.
NODE를 지정하면 그 노드만 처리합니다(병렬 실행에서 실패한 노드만 다시
실행할 때 사용). --from과 NODE는 함께 사용할 수 없습니다.
pathset의 bmc.samePassword와 bmc.parallel 동작은 boot와 같습니다
(upi-forge boot -h 참고).`

func runEject(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("eject", ejectUsage)
	from := fs.String("from", "", "지정한 hostname부터 이어서 처리합니다")
	address := fs.String("address", "", "CSV 대신 직접 지정할 iDRAC 주소")
	username := fs.String("username", "root", "--address와 함께 사용할 iDRAC 계정")
	bmcType := fs.String("bmc-type", config.BMCTypeIDRAC10,
		"--address와 함께 사용할 BMC 종류 ("+strings.Join(config.SupportedBMCTypes, "|")+")")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}

	cfg, err := app.Config()
	if err != nil {
		return err
	}
	wait := time.Duration(cfg.Redfish.MediaSettleSeconds) * time.Second

	if *address == "" {
		// CSV 경로에서는 장비 종류를 pathset의 bmc.type이, 계정을 idracs.csv가
		// 정합니다. --bmc-type/--username을 주면 무시되는 대신 오류로 알려
		// 잘못된 기대를 막습니다.
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		switch {
		case set["bmc-type"]:
			return fmt.Errorf("--bmc-type은 --address와 함께만 사용합니다(CSV 경로는 pathset의 bmc.type을 따름)")
		case set["username"]:
			return fmt.Errorf("--username은 --address와 함께만 사용합니다(CSV 경로는 idracs.csv의 계정을 따름)")
		}
	}

	if *address != "" {
		if *from != "" {
			return fmt.Errorf("--address와 --from은 함께 사용할 수 없습니다")
		}
		if fs.NArg() > 0 {
			return fmt.Errorf("--address와 NODE 인자는 함께 사용할 수 없습니다: %s", fs.Arg(0))
		}
		if !slices.Contains(config.SupportedBMCTypes, *bmcType) {
			return fmt.Errorf("지원하지 않는 --bmc-type입니다: %q (지원: %s)",
				*bmcType, strings.Join(config.SupportedBMCTypes, ", "))
		}
		logx.Section("%s", *address)
		// CSV 경로와 동일하게 환경 변수 폴백을 지원합니다.
		// UPI_FORGE_IDRAC_PASSWORD(공통) 또는 주소를 정규화한 노드별 변수
		// (예: 203.0.113.110 -> UPI_FORGE_IDRAC_PASSWORD_203_0_113_110)를
		// 먼저 찾고, 없으면 숨김 입력을 받습니다.
		password, err := prompt.BMCPassword(*address)
		if err != nil {
			return err
		}
		target := csvdata.IDRAC{Hostname: *address, Address: *address, Username: *username}
		// --address 경로는 pathset과 무관한 직접 지정이라 bmc.type을 알 수
		// 없으므로 --bmc-type(기본 idrac10)으로 드라이버를 고릅니다. CSV
		// 경로와 같은 선택 함수를 거쳐 종류→드라이버 대응을 한곳에 둡니다.
		driver, err := bmcDriverFor(&config.Pathset{Name: "--address", BMC: config.BMC{Type: *bmcType}})
		if err != nil {
			return err
		}
		if err := withSession(ctx, cfg, target, password, func(client *redfish.Client) error {
			return driver.Eject(ctx, client, wait)
		}); err != nil {
			return err
		}
		logx.Blank()
		logx.Info("완료: %s의 Virtual Media를 제거했습니다.", *address)
		if driver.EjectHint != "" {
			logx.Info("%s", driver.EjectHint)
		}
		return nil
	}

	_, ps, err := app.Pathset()
	if err != nil {
		return err
	}
	driver, err := bmcDriverFor(ps)
	if err != nil {
		return err
	}
	_, idracs, targets, err := idracTargets(ps, *from, fs.Args())
	if err != nil {
		return err
	}
	logx.KeyValues("선택 pathset", ps.Name, "대상 노드 수", fmt.Sprint(len(targets)))

	if err := forEachBMCNode(ctx, app, cfg, ps, idracs, targets, []string{"eject"}, fs.NArg() > 0,
		func(_ string, _ csvdata.IDRAC) {
			logx.Info("연결된 Virtual Media를 모두 제거합니다.")
		},
		func(ctx context.Context, _ string, _ csvdata.IDRAC, client *redfish.Client) error {
			return driver.Eject(ctx, client, wait)
		}); err != nil {
		return err
	}

	logx.Blank()
	logx.Info("완료: 모든 노드의 Virtual Media를 제거했습니다.")
	if driver.EjectHint != "" {
		logx.Info("%s", driver.EjectHint)
	}
	return nil
}
