package cli

import (
	"context"
	"fmt"
	"slices"

	"upi-forge/internal/coreos"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/ignition"
	"upi-forge/internal/logx"
)

// fetchCASource는 테스트에서 가짜 MCS 응답을 주입하기 위한 간접 호출입니다.
var fetchCASource = ignition.FetchCASource

const isoUsage = `upi-forge iso - 노드별 설치 ISO를 생성합니다.

기존 03_execution_iso.sh에 해당합니다.

사용법:
  upi-forge iso [--full|--rootfs] [NODE ...]

기본 모드:
  옵션 없이 실행하면 pathset의 iso.useRootfs 값을 사용합니다.
    true  : minimal ISO와 분리 rootfs 모드
    false : full ISO 모드

옵션(이번 실행에만 적용, iso.useRootfs보다 우선):
  --rootfs  rootfs 분리 모드를 사용합니다.
  --full    full ISO 모드를 사용합니다.

NODE를 생략하면 pathset의 nodes.csv 전체를 사용합니다.
기존 <hostname>.iso가 있으면 덮어쓰지 않고 중단합니다.

산출물 출처 검증:
  기존 <hostname>.ign이 현재 설정과 같은 출처인지 확인합니다.
  /etc/hostname 값과 MCS 주소가 현재 설정과 일치해야 하고, Ignition에
  든 MCS 인증서를 현재 클러스터의 MCS 인증서와 대조합니다. 같은
  도메인으로 클러스터를 재구축한 경우 이름이 같아도 인증서가 달라지므로,
  오래된 Ignition으로 ISO를 만드는 사고를 막습니다.
  --skip-ca-check  MCS 접속이 없는 환경에서 인증서 대조만 건너뜁니다
                   (hostname·MCS 주소의 정적 검사는 항상 수행).`

func runISO(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("iso", isoUsage)
	useFull := fs.Bool("full", false, "이번 실행만 full ISO 모드를 사용합니다")
	useRootfs := fs.Bool("rootfs", false, "이번 실행만 rootfs 분리 모드를 사용합니다")
	skipCACheck := fs.Bool("skip-ca-check", false, "기존 Ignition의 MCS 인증서 대조를 건너뜁니다")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if *useFull && *useRootfs {
		return fmt.Errorf("--full과 --rootfs는 함께 사용할 수 없습니다")
	}

	cfg, ps, err := app.Pathset()
	if err != nil {
		return err
	}
	paths, err := cfg.Paths()
	if err != nil {
		return err
	}
	nodes, err := csvdata.LoadNodes(ps.NodesCSV())
	if err != nil {
		return err
	}
	// 다른 클러스터 이름으로 생성된 기존 Ignition을 사용하지 않도록
	// ignition 명령과 같은 도메인 검사를 수행합니다.
	if err := checkNodeDomains(ps.Name, nodes, cfg.Cluster.Domain); err != nil {
		return err
	}
	targets, err := nodes.Select(fs.Args()...)
	if err != nil {
		return err
	}

	rootfsMode := ps.ISO.UseRootfs
	modeSource := "pathset iso.useRootfs"
	switch {
	case *useFull:
		rootfsMode, modeSource = false, "명시적 --full 옵션"
	case *useRootfs:
		rootfsMode, modeSource = true, "명시적 --rootfs 옵션"
	}

	sourceISO := paths.FullISO
	modeName := "full"
	if rootfsMode {
		sourceISO = paths.MinimalISO
		modeName = "rootfs"
	}
	if !fsutil.IsNonEmptyFile(sourceISO) {
		return fmt.Errorf("%s ISO가 없습니다. 먼저 `upi-forge prepare`를 실행하세요: %s", modeName, sourceISO)
	}
	if rootfsMode && cfg.RootfsURL() == "" {
		return fmt.Errorf("rootfs 분리 모드에 필요한 rootfs URL이 비어 있습니다. 설정의 webServer를 확인하세요")
	}

	installer, err := coreos.New(paths.Installer)
	if err != nil {
		return err
	}

	// 노드별 작업을 시작하기 전에 전체 입력과 출력 충돌을 확인합니다.
	// 기존 Ignition은 형식뿐 아니라 출처(어느 노드·어느 클러스터용인지)도
	// 확인합니다. 같은 이름이라도 재구축 전 클러스터의 산출물일 수 있습니다.
	// 구조 검사(hostname, MCS 주소, merge/CA 항목 수, CA 인증서 형식)는
	// 항상 수행합니다. --skip-ca-check는 현재 클러스터의 CA와 대조하는 단계만
	// 건너뜁니다.
	artifactCAs := make(map[string][]string, len(targets))
	for _, host := range targets {
		ignPath := paths.NodeIgnition(host)
		if !fsutil.IsRegularFile(ignPath) {
			return fmt.Errorf("Ignition 파일이 없습니다. 먼저 `upi-forge ignition`을 실행하세요: %s", ignPath)
		}
		art, err := ignition.InspectArtifact(ignPath)
		if err != nil {
			return err
		}
		if art.Hostname != host {
			return fmt.Errorf("기존 Ignition의 hostname이 대상 노드와 다릅니다: %s (Ignition: %s, 대상: %s) — 다른 노드의 산출물입니다. 삭제 후 `upi-forge ignition`을 다시 실행하세요",
				ignPath, art.Hostname, host)
		}
		if art.MCSSource != cfg.MCSSource() {
			return fmt.Errorf("기존 Ignition의 MCS 주소가 현재 설정과 다릅니다: %s (Ignition: %s, 현재: %s) — 다른 클러스터의 산출물입니다. 삭제 후 `upi-forge ignition`을 다시 실행하세요",
				ignPath, art.MCSSource, cfg.MCSSource())
		}
		caFPs, err := ignition.CAFingerprints(art.CASource)
		if err != nil {
			return fmt.Errorf("기존 Ignition의 CA 항목이 올바르지 않습니다: %s: %w", ignPath, err)
		}
		artifactCAs[host] = caFPs
		if !fsutil.IsRegularFile(paths.NodeNMState(host)) {
			return fmt.Errorf("NMState YAML이 없습니다: %s", paths.NodeNMState(host))
		}
		if err := fsutil.MustNotExist(paths.NodeISO(host)); err != nil {
			return err
		}
	}

	// 같은 도메인으로 클러스터를 재구축하면 이름은 같아도 MCS의 CA가
	// 달라집니다. 현재 클러스터의 인증서 체인을 받아 CA 지문을 대조합니다.
	// 서버 인증서는 재구축 없이도 회전될 수 있으므로 체인 전체가 아니라
	// CA 지문만 비교합니다.
	if *skipCACheck {
		logx.Warn("--skip-ca-check로 기존 Ignition의 MCS 인증서 대조를 건너뜁니다.")
	} else {
		currentCA, err := fetchCASource(ctx, cfg.MCSEndpoint())
		if err != nil {
			return fmt.Errorf("MCS 인증서 대조에 실패했습니다 (MCS 접속이 불가능한 환경이면 --skip-ca-check를 사용하세요): %w", err)
		}
		currentFPs, err := ignition.CAFingerprints(currentCA)
		if err != nil {
			return fmt.Errorf("현재 MCS 인증서를 해석하지 못했습니다: %w", err)
		}
		for _, host := range targets {
			if !slices.Equal(artifactCAs[host], currentFPs) {
				return fmt.Errorf("기존 Ignition의 MCS CA가 현재 클러스터와 다릅니다: %s — 클러스터가 재구축되었을 수 있습니다. 기존 산출물(<hostname>.ign/.yaml/.iso)을 삭제하고 `upi-forge ignition`부터 다시 실행하세요",
					paths.NodeIgnition(host))
			}
		}
		logx.Info("MCS 인증서 대조 완료: 기존 Ignition %d개가 현재 클러스터와 일치합니다.", len(targets))
	}

	logx.KeyValues(
		"선택 pathset", ps.Name,
		"ISO 모드", fmt.Sprintf("%s (%s)", modeName, modeSource),
		"원본 ISO", sourceISO,
		"설치 디스크", ps.Disk.OSDisk,
		"대상 노드", fmt.Sprint(targets),
	)
	if rootfsMode {
		logx.Info("rootfs URL   : %s", cfg.RootfsURL())
	}

	for _, host := range targets {
		logx.Section("ISO 생성: %s", host)
		if err := installer.Customize(ctx, coreos.CustomizeOptions{
			SourceISO:  sourceISO,
			Ignition:   paths.NodeIgnition(host),
			NMState:    paths.NodeNMState(host),
			DestDevice: ps.Disk.OSDisk,
			OutputISO:  paths.NodeISO(host),
			// minimal ISO는 이미 customize된 상태이므로 -f가 필요합니다.
			Force: rootfsMode,
		}); err != nil {
			return err
		}
		logx.Info("완료: 노드 설치 ISO를 생성했습니다: %s", paths.NodeISO(host))
	}

	logx.Blank()
	logx.Info("완료: ISO 생성이 끝났습니다.")

	// 다음 단계 안내는 pathset에 idracs.csv가 있는지에 따라 달라집니다.
	// 있으면 iDRAC 부팅 대상(베어메탈)이므로 웹 서버 게시 후 `upi-forge boot`,
	// 없으면 VM 대상으로 간주해 하이퍼바이저에서 ISO를 직접 마운트하도록 안내합니다.
	// 안내 문구만 분기하며 ISO 생성 로직은 두 경우 모두 같습니다.
	if fsutil.IsRegularFile(ps.IDRACsCSV()) {
		logx.Info("다음 단계: 생성된 ISO를 웹 서버에 게시한 뒤 `upi-forge boot`를 실행하세요.")
		for _, host := range targets {
			logx.Info("  %s", cfg.PublishURL(host+".iso"))
		}
		if rootfsMode {
			logx.Info("rootfs 분리 모드입니다. 공용 rootfs 이미지도 함께 게시해야 합니다:")
			logx.Info("  %s", cfg.RootfsURL())
		}
		return nil
	}

	logx.Info("다음 단계: 이 pathset에는 idracs.csv가 없어 VM 대상으로 간주합니다.")
	logx.Info("생성된 ISO를 하이퍼바이저에서 대상 VM의 가상 CD/DVD로 마운트하고, 그 ISO로 부팅하세요.")
	for _, host := range targets {
		logx.Info("  %s", paths.NodeISO(host))
	}
	if rootfsMode {
		// VM 대상이라도 rootfs 분리 모드면 웹 서버가 필요합니다.
		// minimal ISO로 부팅한 노드가 커널 인자의 rootfs URL에서
		// 이미지를 내려받아야 설치가 진행되기 때문입니다.
		logx.Info("rootfs 분리 모드입니다. 부팅한 VM이 다음 주소에서 rootfs를 내려받으므로,")
		logx.Info("웹 서버에 rootfs 이미지를 게시하고 VM에서 그 주소에 접근할 수 있어야 합니다:")
		logx.Info("  %s  (로컬 파일: %s)", cfg.RootfsURL(), paths.RootfsImage)
	}
	logx.Info("부팅하면 ISO에 포함된 Ignition과 NMState 설정으로 %s 디스크에 자동 설치가 진행됩니다.", ps.Disk.OSDisk)
	logx.Info("설치가 끝나면 가상 CD/DVD를 분리해 ISO로 다시 부팅되지 않게 하세요.")
	return nil
}
