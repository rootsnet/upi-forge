// Package release는 OpenShift release에서 machine-os-images 이미지를 찾아
// RHCOS ISO와 coreos-installer를 추출합니다. 이미지 조회와 추출은 연결망과
// 단절망의 인증 및 미러 설정을 처리할 수 있는 oc에 위임합니다.
//
// 추출 결과 검증과 산출물 교체는 다음 규칙에 따라 처리합니다.
//   - ISO와 coreos-installer가 정확히 하나씩인지 확인
//   - 기존 산출물을 백업한 뒤 교체하고, 실패하면 원래대로 복구
package release

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"upi-forge/internal/execx"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/logx"
)

// Options는 이미지 추출에 필요한 값입니다.
type Options struct {
	OCPath       string // oc 실행 파일
	Version      string // 대상 OpenShift 버전 (예: 4.22.5)
	Registry     string // quay.io 또는 mirror registry
	BaseRegistry string // Registry의 첫 경로 요소
	PullSecret   string // pull secret 경로
	TmpDir       string // 추출용 임시 디렉터리
	ISOTarget    string // 최종 full ISO 경로
	InstallerTgt string // 최종 coreos-installer 경로
}

// ImageFor는 대상 버전의 machine-os-images 이미지 참조를 조회합니다.
func ImageFor(ctx context.Context, o Options) (string, error) {
	var releaseImage string
	if o.BaseRegistry == "quay.io" {
		releaseImage = fmt.Sprintf("quay.io/openshift-release-dev/ocp-release:%s-x86_64", o.Version)
	} else {
		releaseImage = fmt.Sprintf("%s/openshift/release-images:%s-x86_64", o.Registry, o.Version)
	}

	out, err := execx.Output(ctx, o.OCPath, "adm", "release", "info",
		releaseImage,
		"--image-for=machine-os-images",
		"--registry-config="+o.PullSecret)
	if err != nil {
		return "", fmt.Errorf("machine-os-images를 확인하지 못했습니다: version=%s: %w", o.Version, err)
	}
	image := strings.TrimSpace(out)
	if image == "" {
		return "", fmt.Errorf("machine-os-images를 확인하지 못했습니다: version=%s", o.Version)
	}

	// mirror registry를 쓰는 단절 환경에서는 art-dev 경로를 mirror 경로로 바꿉니다.
	if !strings.HasPrefix(image, o.BaseRegistry) {
		image = strings.Replace(image,
			"quay.io/openshift-release-dev/ocp-v4.0-art-dev",
			o.Registry+"/openshift/release", 1)
	}
	return image, nil
}

// Extract는 machine-os-images에서 RHCOS ISO와 coreos-installer를 추출해
// 최종 경로로 교체합니다. 임시 디렉터리는 성공/실패와 무관하게 정리합니다.
func Extract(ctx context.Context, o Options) error {
	if o.Version == "" {
		return fmt.Errorf("OpenShift 버전이 비어 있습니다")
	}
	if o.Registry == "" || o.BaseRegistry == "" {
		return fmt.Errorf("레지스트리 설정이 비어 있습니다")
	}
	if !fsutil.IsRegularFile(o.PullSecret) {
		return fmt.Errorf("pull secret을 읽을 수 없습니다: %s", o.PullSecret)
	}
	if _, err := execx.Require(o.OCPath); err != nil {
		return err
	}

	logx.Info("정보: 대상 OpenShift 버전: %s", o.Version)

	image, err := ImageFor(ctx, o)
	if err != nil {
		return err
	}
	logx.Info("정보: 추출 이미지: %s", image)

	if err := os.RemoveAll(o.TmpDir); err != nil {
		return fmt.Errorf("임시 디렉터리를 정리하지 못했습니다: %s: %w", o.TmpDir, err)
	}
	if err := os.MkdirAll(o.TmpDir, 0o755); err != nil {
		return fmt.Errorf("임시 디렉터리를 만들지 못했습니다: %s: %w", o.TmpDir, err)
	}
	defer func() {
		if err := os.RemoveAll(o.TmpDir); err == nil {
			logx.Info("임시 디렉터리 삭제 완료: %s", o.TmpDir)
		}
	}()

	logx.Info("정보: machine-os-images를 임시 디렉터리에 추출합니다: %s", o.TmpDir)
	if err := execx.Run(ctx, o.OCPath, "image", "extract", image,
		"--path=/:"+o.TmpDir,
		"--registry-config="+o.PullSecret,
		"--confirm"); err != nil {
		return fmt.Errorf("machine-os-images 추출에 실패했습니다: %w", err)
	}

	isoFiles, err := findFiles(o.TmpDir, func(name string) bool {
		return strings.HasSuffix(name, ".iso")
	})
	if err != nil {
		return err
	}
	installerFiles, err := findFiles(o.TmpDir, func(name string) bool {
		return name == "coreos-installer"
	})
	if err != nil {
		return err
	}
	if len(isoFiles) != 1 {
		return fmt.Errorf("RHCOS ISO가 정확히 하나여야 합니다: 발견=%d", len(isoFiles))
	}
	if len(installerFiles) != 1 {
		return fmt.Errorf("coreos-installer가 정확히 하나여야 합니다: 발견=%d", len(installerFiles))
	}

	// 임시 디렉터리는 별도 파일 시스템일 수 있으므로 rename 전에
	// 최종 경로와 같은 디렉터리로 복사해 둡니다.
	stageISO := filepath.Join(filepath.Dir(o.ISOTarget), ".upi-forge-stage-iso")
	stageInstaller := filepath.Join(filepath.Dir(o.InstallerTgt), ".upi-forge-stage-installer")
	if err := fsutil.CopyFile(isoFiles[0], stageISO, 0o644); err != nil {
		return err
	}
	if err := fsutil.CopyFile(installerFiles[0], stageInstaller, 0o755); err != nil {
		os.Remove(stageISO)
		return err
	}

	repl := fsutil.NewReplacement()
	defer repl.Cleanup()
	repl.Add(stageISO, o.ISOTarget, 0o644)
	repl.Add(stageInstaller, o.InstallerTgt, 0o755)
	if err := repl.Commit(); err != nil {
		return err
	}

	logx.Info("완료: RHCOS ISO와 coreos-installer를 준비했습니다.")
	logx.Info("      ISO              : %s", o.ISOTarget)
	logx.Info("      coreos-installer : %s", o.InstallerTgt)
	return nil
}

// findFiles는 root 아래에서 조건에 맞는 일반 파일을 찾습니다.
func findFiles(root string, match func(name string) bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if match(d.Name()) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("추출 결과를 확인하지 못했습니다: %s: %w", root, err)
	}
	return out, nil
}
