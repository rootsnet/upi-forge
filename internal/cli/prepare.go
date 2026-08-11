package cli

import (
	"context"
	"fmt"

	"upi-forge/internal/coreos"
	"upi-forge/internal/logx"
	"upi-forge/internal/preflight"
	"upi-forge/internal/release"
)

const prepareUsage = `upi-forge prepare - RHCOS 이미지와 도구를 준비합니다.

기존 01_preparation_image_and_tool.sh에 해당합니다.

수행 내용:
  1) OpenShift release에서 machine-os-images를 찾아
     RHCOS full ISO와 coreos-installer를 추출합니다.
  2) full ISO에서 minimal ISO와 분리 rootfs를 만듭니다.

full ISO 모드만 사용할 예정이어도 두 ISO 모드의 공통 준비를 위해
minimal ISO와 rootfs를 함께 만듭니다.

설정의 cluster.version이 비어 있으면 현재 oc 로그인 클러스터의 버전을
조회하며, 그 전에 대상 클러스터가 맞는지 사전 점검합니다.`

func runPrepare(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("prepare", prepareUsage)
	skipSplit := fs.Bool("skip-rootfs-split", false,
		"minimal ISO와 rootfs 분리 단계를 건너뜁니다")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("인자를 지원하지 않습니다: %s", fs.Arg(0))
	}

	cfg, err := app.Config()
	if err != nil {
		return err
	}
	paths, err := cfg.Paths()
	if err != nil {
		return err
	}

	version := cfg.Cluster.Version
	if version == "" {
		// 현재 로그인 클러스터에서 버전을 가져올 때만 대상 클러스터 일치를 먼저 확인합니다.
		if _, err := preflight.Check(ctx, cfg.Tools.OC, cfg.Cluster.APIURL); err != nil {
			return err
		}
		if version, err = preflight.ClusterVersion(ctx, cfg.Tools.OC); err != nil {
			return err
		}
	}

	steps := 2
	if *skipSplit {
		steps = 1
	}
	logx.BeginSteps(steps)

	logx.Step("RHCOS ISO와 coreos-installer 준비")
	if err := release.Extract(ctx, release.Options{
		OCPath:       cfg.Tools.OC,
		Version:      version,
		Registry:     cfg.Registry.Registry,
		BaseRegistry: cfg.BaseRegistry(),
		PullSecret:   cfg.PullSecretPath(),
		TmpDir:       paths.TmpDir,
		ISOTarget:    paths.FullISO,
		// 추출 대상은 항상 작업 디렉터리입니다. tools.coreosInstaller로
		// 외부 도구를 지정한 경우에도 그 경로(/usr/bin/... 등)를 추출
		// 대상으로 삼으면 시스템 바이너리를 덮어쓰게 됩니다.
		InstallerTgt: paths.WorkspaceInstaller,
	}); err != nil {
		return err
	}

	if *skipSplit {
		logx.Blank()
		logx.Info("완료: 이미지와 도구 준비가 끝났습니다. (rootfs 분리 생략)")
		return nil
	}

	logx.Blank()
	logx.Step("minimal ISO와 rootfs 준비")
	installer, err := coreos.New(paths.Installer)
	if err != nil {
		return err
	}
	if err := installer.SplitRootfs(ctx, paths.FullISO, paths.MinimalISO,
		paths.RootfsImage, cfg.RootfsURL()); err != nil {
		return err
	}

	logx.Blank()
	logx.Info("완료: 이미지와 도구 준비가 끝났습니다.")
	logx.Info("rootfs 분리 모드를 사용한다면 다음 파일을 웹 서버에 게시하세요:")
	logx.Info("  %s", cfg.RootfsURL())
	return nil
}
