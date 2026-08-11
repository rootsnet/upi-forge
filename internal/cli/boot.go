package cli

import (
	"context"
	"fmt"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

const bootUsage = `upi-forge boot - 노드별 설치 ISO를 iDRAC Virtual Media로 부팅합니다.

기존 04_execution_idrac10_boot.sh에 해당합니다.

사용법:
  upi-forge boot [--from HOSTNAME]

인자 없이 실행하면 선택한 pathset의 nodes.csv 첫 노드부터 처리합니다.
--from은 지정한 노드부터 마지막 노드까지 이어서 처리합니다.

선행 단계:
  upi-forge iso 로 노드별 ISO를 만들고 웹 서버에 게시해야 합니다.

iDRAC 비밀번호는 노드별로 화면에서 숨겨 입력합니다.
자동화 환경에서는 UPI_FORGE_IDRAC_PASSWORD 환경 변수를 사용할 수 있습니다.

rootfs 분리 여부는 ISO 생성 단계에서 이미 반영되므로 이 명령에서는 구분하지 않습니다.`

func runBoot(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("boot", bootUsage)
	from := fs.String("from", "", "지정한 hostname부터 이어서 처리합니다")
	skipISOCheck := fs.Bool("skip-iso-check", false, "ISO URL 사전 접근 확인을 건너뜁니다")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("지원하지 않는 인자입니다: %s", fs.Arg(0))
	}

	cfg, ps, err := app.Pathset()
	if err != nil {
		return err
	}
	paths, err := cfg.Paths()
	if err != nil {
		return err
	}
	driver, err := bmcDriverFor(ps)
	if err != nil {
		return err
	}
	nodes, idracs, targets, err := idracTargets(ps, *from)
	if err != nil {
		return err
	}
	// 검사 추가 이전에 만들어진 다른 클러스터 이름의 설치 ISO를
	// 부팅하지 않도록, ignition/iso와 같은 도메인 검사를 여기서도 합니다.
	if err := checkNodeDomains(ps.Name, nodes, cfg.Cluster.Domain); err != nil {
		return err
	}

	// iDRAC 작업을 시작하기 전에 이번 실행 대상의 ISO를 모두 확인합니다.
	for _, host := range targets {
		if !fsutil.IsRegularFile(paths.NodeISO(host)) {
			return fmt.Errorf("노드 설치 ISO가 없습니다. 먼저 `upi-forge iso`를 실행하세요: %s",
				paths.NodeISO(host))
		}
	}

	logx.KeyValues("선택 pathset", ps.Name, "대상 노드 수", fmt.Sprint(len(targets)))

	waits := waitsFrom(cfg)
	timeout := time.Duration(cfg.Redfish.TimeoutSeconds) * time.Second

	// 재개 안내가 원래 실행과 같은 옵션을 포함하도록 명령 이름에 플래그를 담습니다.
	resumeName := "boot"
	if *skipISOCheck {
		resumeName += " --skip-iso-check"
	}
	err = forEachBMCNode(ctx, app, cfg, idracs, targets, resumeName,
		func(host string, _ csvdata.IDRAC) {
			// 비밀번호를 묻기 전에 어떤 장비에 어떤 ISO를 붙일지 보여줍니다.
			logx.Info("설치 ISO: %s", cfg.PublishURL(host+".iso"))
		},
		func(ctx context.Context, host string, _ csvdata.IDRAC, client *redfish.Client) error {
			return driver.Boot(ctx, client, bmc.BootRequest{
				ISOURL:       cfg.PublishURL(host + ".iso"),
				Waits:        waits,
				SkipISOCheck: *skipISOCheck,
				HTTPTimeout:  timeout,
			})
		})
	if err != nil {
		return err
	}

	logx.Blank()
	logx.Info("완료: 모든 노드에 설치 ISO 부팅을 요청했습니다.")
	logx.Info("설치 상태는 %s과 `oc get nodes`, `oc get csr`로 확인하세요.", driver.ConsoleName)
	return nil
}
