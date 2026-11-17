// Package release는 OpenShift release에서 machine-os-images 이미지를 찾아
// RHCOS ISO와 coreos-installer를 추출합니다. 이미지 조회와 추출은 oc에
// 위임하되, 폐쇄망(disconnected) 환경에서 원본 인터넷 주소가 나오는 경우
// 클러스터 미러 설정(internal/mirror)으로 만든 후보 주소를 순서대로
// 시도합니다.
//
// 추출 결과 검증과 산출물 교체는 다음 규칙에 따라 처리합니다.
//   - ISO와 coreos-installer가 정확히 하나씩인지 확인합니다.
//   - 기존 산출물을 백업한 뒤 교체하고, 실패하면 원래대로 복구합니다.
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
	"upi-forge/internal/mirror"
)

// Options는 이미지 추출에 필요한 값입니다.
type Options struct {
	OCPath       string       // oc 실행 파일
	Version      string       // 대상 OpenShift 버전 (메시지·구성 폴백용)
	Registry     string       // quay.io 또는 mirror registry
	BaseRegistry string       // Registry의 첫 경로 요소
	PullSecret   string       // pull secret 경로
	TmpDir       string       // 추출용 임시 디렉터리
	ISOTarget    string       // 최종 full ISO 경로
	InstallerTgt string       // 최종 coreos-installer 경로
	ReleaseImage string       // 사용할 release 이미지 참조(클러스터 조회값 또는 구성값)
	Sets         *mirror.Sets // 검증된 클러스터의 미러 설정. nil이면 오프라인(미검증)
}

// ReleaseImageRef는 버전과 레지스트리로 release 이미지 참조를 구성합니다.
// 클러스터에서 실제 desired 이미지를 읽지 못할 때의 폴백입니다.
func ReleaseImageRef(version, registry, baseRegistry string) string {
	if baseRegistry == "quay.io" {
		return fmt.Sprintf("quay.io/openshift-release-dev/ocp-release:%s-x86_64", version)
	}
	return fmt.Sprintf("%s/openshift/release-images:%s-x86_64", registry, version)
}

// candidates는 이미지 참조에 적용할 후보 주소를 순서대로 반환합니다.
//
//   - 검증된 클러스터의 미러 설정(Sets)에 매칭되는 규칙이 있으면 적용합니다.
//   - 설정 조회·해석에 실패하지 않았고 매칭되는 규칙이 없으면 원본을 사용합니다.
//   - Sets가 없거나, 설정 조회·해석에 실패했고 매칭되는 규칙도 없으면
//     고정 경로 치환(fallback)을 적용합니다.
func (o Options) candidates(ref string) []string {
	if o.Sets != nil {
		cands, matched := o.Sets.Resolve(ref)
		if matched || o.Sets.Reliable() {
			return cands
		}
		// 읽지 못한 미러 설정에 규칙이 있을 수 있으므로 고정 경로 치환을 적용합니다.
	}
	return []string{o.fallback(ref)}
}

// fallback은 클러스터 미러 규칙을 적용할 수 없을 때 고정 경로 치환을 수행합니다.
// release 이미지와 release payload(machine-os-images 등 art-dev)를
// 프로젝트에서 사용하는 미러 저장소 경로로 각각 바꿉니다.
// 이미 미러(설정 registry) 주소이거나 알 수 없는 참조는 원본 그대로 둡니다.
// 다이제스트·태그 접미사는 그대로 유지합니다.
func (o Options) fallback(ref string) string {
	if strings.HasPrefix(ref, o.BaseRegistry) {
		return ref // 이미 미러 주소
	}
	// release 이미지: quay.io/.../ocp-release → <registry>/openshift/release-images
	// ReleaseImageRef와 같은 저장소 경로를 사용합니다.
	if r := strings.Replace(ref,
		"quay.io/openshift-release-dev/ocp-release",
		o.Registry+"/openshift/release-images", 1); r != ref {
		return r
	}
	// release payload(art-dev, machine-os-images 포함):
	//   quay.io/.../ocp-v4.0-art-dev → <registry>/openshift/release
	if r := strings.Replace(ref,
		"quay.io/openshift-release-dev/ocp-v4.0-art-dev",
		o.Registry+"/openshift/release", 1); r != ref {
		return r
	}
	return ref
}

// machineOSRef는 release 이미지 후보를 순서대로 시도해 machine-os-images
// 참조를 얻습니다.
func machineOSRef(ctx context.Context, o Options) (string, error) {
	cands := o.candidates(o.ReleaseImage)
	var lastErr error
	for i, ref := range cands {
		out, err := execx.Output(ctx, o.OCPath, "adm", "release", "info",
			ref,
			"--image-for=machine-os-images",
			"--registry-config="+o.PullSecret)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = err
			if len(cands) > 1 {
				logx.Warn("release 이미지 후보 실패(%d/%d), 다음 후보를 시도합니다: %s: %v",
					i+1, len(cands), ref, err)
			}
			continue
		}
		image := strings.TrimSpace(out)
		if image == "" {
			lastErr = fmt.Errorf("빈 machine-os-images 참조: %s", ref)
			continue
		}
		return image, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("후보가 없습니다")
	}
	return "", fmt.Errorf("machine-os-images를 확인하지 못했습니다: version=%s: %w", o.Version, lastErr)
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
	if o.ReleaseImage == "" {
		return fmt.Errorf("release 이미지 참조가 비어 있습니다")
	}
	if !fsutil.IsRegularFile(o.PullSecret) {
		return fmt.Errorf("pull secret을 읽을 수 없습니다: %s", o.PullSecret)
	}
	if _, err := execx.Require(o.OCPath); err != nil {
		return err
	}

	logx.Info("정보: 대상 OpenShift 버전: %s", o.Version)

	mos, err := machineOSRef(ctx, o)
	if err != nil {
		return err
	}
	cands := o.candidates(mos)
	logx.Info("정보: 추출 이미지: %s", cands[0])

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

	var extractErr error
	extracted := false
	for i, ref := range cands {
		// 각 후보를 시도하기 전에 임시 디렉터리를 비워 이전 후보의 부분 산출물을 제거합니다.
		if err := clearDir(o.TmpDir); err != nil {
			return err
		}
		if len(cands) > 1 {
			logx.Info("정보: machine-os-images 추출 시도(%d/%d): %s", i+1, len(cands), ref)
		} else {
			logx.Info("정보: machine-os-images를 임시 디렉터리에 추출합니다: %s", o.TmpDir)
		}
		if err := execx.Run(ctx, o.OCPath, "image", "extract", ref,
			"--path=/:"+o.TmpDir,
			"--registry-config="+o.PullSecret,
			"--confirm"); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			extractErr = err
			if len(cands) > 1 {
				logx.Warn("추출 후보 실패(%d/%d), 다음 후보를 시도합니다: %s: %v", i+1, len(cands), ref, err)
			}
			continue
		}
		extracted = true
		break
	}
	if !extracted {
		return fmt.Errorf("machine-os-images 추출에 실패했습니다: %w", extractErr)
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

// clearDir는 디렉터리 자체는 유지하고 그 안의 내용을 비웁니다.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("임시 디렉터리를 읽지 못했습니다: %s: %w", dir, err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("임시 디렉터리를 비우지 못했습니다: %s: %w", dir, err)
		}
	}
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
