package cli

import (
	"context"
	"fmt"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

const liveBootUsage = `upi-forge live-boot - 공용 CoreOS ISO로 라이브 부팅합니다.

기존 pathset-3/01_execution_idrac10_live_boot.sh에 해당합니다.

목적은 설치가 아니라 설치 전 하드웨어 조사입니다. 작업 전체에서 가장 먼저,
pathset(하드웨어 그룹)마다 기본 이미지(공용 coreos-x86_64.iso)로 부팅해
NIC 이름과 디스크 by-path를 확인하고, 그 값으로 pathset.yaml과 nics.csv를
작성합니다. iDRAC이 보고하는 참고 정보를 먼저 보려면 upi-forge inventory를
함께 사용합니다.

이 명령은 iDRAC 전용이라 베어메탈에만 해당합니다. VM은 하이퍼바이저에서
기본 이미지를 가상 CD/DVD로 마운트해 부팅한 뒤 같은 방법으로 확인합니다.

라이브 환경(이머전시 모드까지만 진입해도 됩니다)에서 다음을 확인합니다.

  ip -br link
  ls -l /sys/class/net/
  ls -l /dev/disk/by-path/

사용법:
  upi-forge live-boot [--from HOSTNAME]

선행 단계:
  upi-forge prepare 로 만든 full ISO를 웹 서버에 게시해야 합니다.
  설치 산출물(<hostname>.ign, <hostname>.iso)은 필요하지 않습니다.

pathset의 iso.useRootfs 값과 무관하게 항상 공용 full ISO로 부팅합니다.
아직 실측 전이라 확정되지 않은 pathset.yaml의 osDisk/NIC 값은 임시값이어도
됩니다. 이 명령은 그 값들을 사용하지 않고 nodes.csv/idracs.csv만 읽습니다.
조사 대상 pathset은 전역 옵션 --pathset으로 지정할 수 있습니다.`

func runLiveBoot(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("live-boot", liveBootUsage)
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
	driver, err := bmcDriverFor(ps)
	if err != nil {
		return err
	}
	_, idracs, targets, err := idracTargets(ps, *from)
	if err != nil {
		return err
	}

	isoURL := cfg.PublishURL(cfg.Workspace.FullISO)
	logx.KeyValues(
		"선택 pathset", ps.Name,
		"라이브 ISO", isoURL,
		"대상 노드 수", fmt.Sprint(len(targets)),
	)

	waits := waitsFrom(cfg)
	timeout := time.Duration(cfg.Redfish.TimeoutSeconds) * time.Second

	// 재개 안내가 원래 실행과 같은 옵션을 포함하도록 명령 이름에 플래그를 담습니다.
	resumeName := "live-boot"
	if *skipISOCheck {
		resumeName += " --skip-iso-check"
	}
	err = forEachBMCNode(ctx, app, cfg, idracs, targets, resumeName,
		func(_ string, _ csvdata.IDRAC) {
			logx.Info("라이브 ISO: %s", isoURL)
		},
		func(ctx context.Context, _ string, _ csvdata.IDRAC, client *redfish.Client) error {
			return driver.Boot(ctx, client, bmc.BootRequest{
				ISOURL:       isoURL,
				Waits:        waits,
				SkipISOCheck: *skipISOCheck,
				HTTPTimeout:  timeout,
			})
		})
	if err != nil {
		return err
	}

	logx.Blank()
	logx.Info("완료: 모든 노드에 CoreOS 라이브 부팅을 요청했습니다.")
	logx.Info("%s에서 NIC 이름과 디스크 by-path를 확인한 뒤", driver.ConsoleName)
	logx.Info("pathset.yaml과 nics.csv에 반영하세요.")
	return nil
}
