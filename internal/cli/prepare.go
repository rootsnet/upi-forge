package cli

import (
	"context"
	"fmt"

	"upi-forge/internal/coreos"
	"upi-forge/internal/logx"
	"upi-forge/internal/mirror"
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

	// 대상 클러스터(cluster.apiURL)와 현재 oc 로그인 대상이 일치하는지
	// 먼저 확인합니다. 라이브 클러스터의 desired 이미지나 미러 설정은
	// 이 확인을 통과한 경우에만 사용해 다른 클러스터의 미러 규칙이
	// 적용되지 않도록 합니다.
	version, releaseImage, sets, err := resolvePrepareInputs(ctx,
		cfg.Cluster.Version, cfg.Registry.Registry, cfg.BaseRegistry(),
		clusterProbe{
			check:        func(c context.Context) error { _, e := preflight.Check(c, cfg.Tools.OC, cfg.Cluster.APIURL); return e },
			version:      func(c context.Context) (string, error) { return preflight.ClusterVersion(c, cfg.Tools.OC) },
			loadSets:     func(c context.Context) (*mirror.Sets, error) { return mirror.Load(c, cfg.Tools.OC) },
			desiredImage: func(c context.Context) (string, error) { return preflight.DesiredImage(c, cfg.Tools.OC) },
		})
	if err != nil {
		return err
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
		ReleaseImage: releaseImage,
		Sets:         sets,
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

// clusterProbe는 클러스터 확인과 조회에 사용하는 함수 모음입니다.
// 테스트에서는 각 함수를 대체해 oc 없이 resolvePrepareInputs를 검증합니다.
type clusterProbe struct {
	check        func(context.Context) error                 // 대상 클러스터 일치 확인
	version      func(context.Context) (string, error)       // 클러스터 OpenShift 버전
	loadSets     func(context.Context) (*mirror.Sets, error) // 미러 설정 로드
	desiredImage func(context.Context) (string, error)       // status.desired.image
}

// resolvePrepareInputs는 대상 클러스터 검증 결과에 따라 사용할 버전,
// release 이미지 참조, 미러 설정을 결정합니다.
//
//   - 검증 통과(같은 클러스터 로그인): 클러스터의 버전·desired 이미지·미러
//     설정을 사용합니다. 명시한 cfgVersion이 클러스터 버전과 다르면 임의로
//     대체하지 않고 오류로 중단합니다.
//   - 검증에 실패하고 cfgVersion이 지정된 경우: 클러스터의 미러 설정을
//     사용하지 않고(sets=nil), 지정한 버전으로 오프라인 준비를 진행합니다.
//   - 검증에 실패하고 cfgVersion이 없는 경우: 사용할 버전을 결정할 수 없으므로
//     오류로 중단합니다.
func resolvePrepareInputs(ctx context.Context, cfgVersion, registry, baseRegistry string, p clusterProbe) (version, releaseImage string, sets *mirror.Sets, err error) {
	version = cfgVersion
	if cerr := p.check(ctx); cerr == nil {
		actual, verr := p.version(ctx)
		if verr != nil {
			return "", "", nil, verr
		}
		if version != "" && version != actual {
			return "", "", nil, fmt.Errorf("설정의 cluster.version(%s)과 클러스터가 보고한 버전(%s)이 "+
				"다릅니다.\n      잘못된 이미지를 준비하지 않도록 중단합니다. 설정을 맞추거나 "+
				"cluster.version을 비우세요", version, actual)
		}
		version = actual

		s, lerr := p.loadSets(ctx)
		if lerr != nil {
			return "", "", nil, lerr // 상위 컨텍스트 취소(Ctrl+C 등)
		}
		sets = s

		if img, ierr := p.desiredImage(ctx); ierr == nil && img != "" {
			logx.Info("정보: 클러스터 desired release 이미지: %s", img)
			releaseImage = img
		} else if ierr != nil {
			logx.Warn("클러스터의 desired release 이미지를 읽지 못해 버전으로 구성합니다: %v", ierr)
		}
	} else if version == "" {
		return "", "", nil, cerr
	} else {
		logx.Warn("대상 클러스터를 확인하지 못했습니다(미로그인 또는 다른 클러스터). "+
			"cluster.version=%s로 오프라인 준비를 진행하며, 클러스터 미러 설정(IDMS/ITMS)은 "+
			"적용하지 않습니다.", version)
		logx.Warn("  사유: %v", cerr)
	}
	if releaseImage == "" {
		releaseImage = release.ReleaseImageRef(version, registry, baseRegistry)
	}
	return version, releaseImage, sets, nil
}
