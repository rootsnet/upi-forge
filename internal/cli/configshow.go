package cli

import (
	"context"
	"fmt"
	"strings"

	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/logx"
)

const configUsage = `upi-forge config - 읽어 들인 설정과 계산된 값을 확인합니다.

실행 전에 어떤 클러스터, 어떤 pathset, 어떤 경로가 사용되는지 점검하는 용도입니다.
외부 시스템(OpenShift, iDRAC, 웹 서버)에 접속하지 않습니다.`

func runConfigShow(_ context.Context, app *App, args []string) error {
	fs := newFlagSet("config", configUsage)
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("지원하지 않는 인자입니다 (pathset은 전역 옵션 --pathset으로 지정): %s", fs.Arg(0))
	}

	cfg, err := app.Config()
	if err != nil {
		return err
	}
	paths, err := cfg.Paths()
	if err != nil {
		return err
	}

	logx.Section("설정")
	logx.KeyValues(
		"설정 파일", cfg.Path(),
		"작업 디렉터리", paths.Dir,
		"클러스터 도메인", cfg.Cluster.Domain,
		"API 주소", cfg.Cluster.APIURL,
		"MCS", cfg.MCSSource(),
		"OpenShift 버전", orAuto(cfg.Cluster.Version),
		"레지스트리", cfg.Registry.Registry,
		"pull secret", pathState(cfg.PullSecretPath()),
		"웹 서버", cfg.WebServer.URL+cfg.WebServer.Path,
		"rootfs URL", cfg.RootfsURL(),
		"Ignition 템플릿", pathState(cfg.TemplatePath()),
	)

	logx.Section("산출물 경로")
	logx.KeyValues(
		"full ISO", pathState(paths.FullISO),
		"minimal ISO", pathState(paths.MinimalISO),
		"rootfs", pathState(paths.RootfsImage),
		"coreos-installer", pathState(paths.Installer),
	)

	_, ps, err := app.Pathset()
	if err != nil {
		return err
	}
	logx.Section("pathset: %s", ps.Name)
	network := string(ps.Network.Source)
	if ps.Network.Bonding {
		network += fmt.Sprintf(" / bond %s(%s: %s, %s)", ps.Network.Bond.Name,
			ps.Network.Bond.Mode, ps.Network.Bond.Primary, ps.Network.Bond.Standby)
	} else {
		network += " / single " + ps.Network.ActiveNIC
	}
	logx.KeyValues(
		"설명", orDash(ps.Description),
		"디렉터리", ps.Dir(),
		"ISO 모드", isoModeName(ps.ISO.UseRootfs),
		"설치 디스크", ps.Disk.OSDisk,
		"네트워크", network,
		"게이트웨이", fmt.Sprintf("%s/%d", ps.Network.Gateway, ps.Network.PrefixLength),
		"DNS", strings.Join(ps.Network.DNS, ", "),
		"Butane", pathState(ps.ButanePath()),
		"nodes.csv", pathState(ps.NodesCSV()),
		"nics.csv", pathState(ps.NICsCSV()),
		"idracs.csv", pathState(ps.IDRACsCSV()),
		"BMC 종류", bmcTypeDisplay(ps),
		"BMC 실행", bmcRunDisplay(ps),
	)

	if nodes, err := csvdata.LoadNodes(ps.NodesCSV()); err == nil {
		logx.Section("노드 (%d개)", len(nodes.Items))
		for _, node := range nodes.Items {
			logx.Info("  %-32s %-15s ign=%s iso=%s",
				node.Hostname, node.IP,
				existsMark(paths.NodeIgnition(node.Hostname)),
				existsMark(paths.NodeISO(node.Hostname)))
		}
	} else {
		logx.Warn("nodes.csv를 읽지 못했습니다: %v", err)
	}
	return nil
}

func isoModeName(useRootfs bool) string {
	if useRootfs {
		return "rootfs (minimal ISO + 분리 rootfs)"
	}
	return "full"
}

// bmcTypeDisplay는 pathset의 BMC 종류 표시 문자열입니다.
// bmc.type은 idracs.csv가 있는 베어메탈 pathset에서만 의미가 있으므로,
// 파일이 없는 VM pathset에는 기본값 대신 해당 없음을 표시합니다.
func bmcTypeDisplay(ps *config.Pathset) string {
	if !fsutil.IsRegularFile(ps.IDRACsCSV()) {
		return "(해당 없음: idracs.csv 없음)"
	}
	return ps.BMC.Type
}

// bmcRunDisplay는 BMC 명령의 실행 방식(순차/병렬, 비밀번호 입력 방식)
// 표시 문자열입니다. bmcTypeDisplay와 같은 이유로 idracs.csv가 없는
// pathset에는 해당 없음을 표시합니다.
func bmcRunDisplay(ps *config.Pathset) string {
	if !fsutil.IsRegularFile(ps.IDRACsCSV()) {
		return "(해당 없음: idracs.csv 없음)"
	}
	run := "순차"
	if ps.BMC.Parallel >= 2 {
		run = fmt.Sprintf("병렬 (동시 최대 %d개)", ps.BMC.Parallel)
	}
	password := "비밀번호 노드별 입력"
	if ps.BMC.SamePassword {
		password = "공통 비밀번호 1회 입력"
	}
	return run + " / " + password
}

func pathState(path string) string {
	if path == "" {
		return "(설정 없음)"
	}
	if fsutil.Exists(path) {
		return path + "  [있음]"
	}
	return path + "  [없음]"
}

func existsMark(path string) string {
	if fsutil.Exists(path) {
		return "O"
	}
	return "-"
}

func orAuto(s string) string {
	if s == "" {
		return "(비어 있음: 현재 클러스터에서 자동 조회)"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
