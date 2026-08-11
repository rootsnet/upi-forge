// Package coreos는 RHCOS ISO의 embedded Ignition과 커널 인자를 처리하는
// coreos-installer 바이너리를 감쌉니다.
package coreos

import (
	"context"
	"fmt"
	"os"
	"strings"

	"upi-forge/internal/execx"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/logx"
)

// Installer는 coreos-installer 실행 파일 경로를 담습니다.
type Installer struct {
	Path string
}

// New는 실행 가능한 coreos-installer를 확인하고 래퍼를 만듭니다.
func New(path string) (*Installer, error) {
	if !fsutil.IsExecutable(path) {
		return nil, fmt.Errorf("coreos-installer가 없거나 실행할 수 없습니다. "+
			"먼저 `upi-forge prepare`를 실행하세요: %s", path)
	}
	return &Installer{Path: path}, nil
}

// SplitRootfs는 full ISO에서 minimal ISO와 분리 rootfs를 만듭니다.
// 기존 minimal ISO와 rootfs는 삭제한 뒤 새로 생성하며, 원본 ISO는 바꾸지 않습니다.
func (i *Installer) SplitRootfs(ctx context.Context, sourceISO, minimalISO, rootfsOut, rootfsURL string) error {
	if rootfsURL == "" {
		return fmt.Errorf("설정의 webServer 값으로 만든 rootfs URL이 비어 있습니다")
	}
	if !fsutil.IsNonEmptyFile(sourceISO) {
		return fmt.Errorf("원본 ISO가 없습니다. 먼저 이미지 추출 단계를 확인하세요: %s", sourceISO)
	}

	logx.Info("정보: 기존 minimal ISO와 rootfs가 있으면 삭제합니다.")
	_ = os.Remove(minimalISO)
	_ = os.Remove(rootfsOut)

	logx.Info("정보: minimal ISO와 rootfs를 생성합니다. 수 분이 걸릴 수 있습니다.")
	logx.Info("      원본        : %s", sourceISO)
	logx.Info("      minimal ISO : %s", minimalISO)
	logx.Info("      rootfs      : %s", rootfsOut)
	logx.Info("      rootfs URL  : %s", rootfsURL)

	cleanup := func() {
		_ = os.Remove(minimalISO)
		_ = os.Remove(rootfsOut)
	}

	if err := execx.Run(ctx, i.Path, "iso", "extract", "minimal-iso",
		"--output-rootfs", rootfsOut,
		"--rootfs-url", rootfsURL,
		sourceISO, minimalISO); err != nil {
		cleanup()
		return fmt.Errorf("minimal ISO와 rootfs 추출에 실패했습니다. "+
			"원본 ISO가 이미 customize된 것은 아닌지 확인하세요: %w", err)
	}

	kargs, err := i.KernelArgs(ctx, minimalISO)
	if err != nil {
		cleanup()
		return err
	}
	found := ""
	for _, arg := range kargs {
		if strings.HasPrefix(arg, "coreos.live.rootfs_url=") {
			found = arg
			break
		}
	}
	if found == "" {
		cleanup()
		return fmt.Errorf("minimal ISO에 coreos.live.rootfs_url 커널 인자가 없습니다: %s", minimalISO)
	}
	logx.Info("\n== 확인: minimal ISO에 설정된 rootfs URL ==")
	logx.Info("%s", found)

	if !fsutil.IsNonEmptyFile(minimalISO) || !fsutil.IsNonEmptyFile(rootfsOut) {
		cleanup()
		return fmt.Errorf("minimal ISO 또는 rootfs 결과가 비어 있습니다")
	}
	logx.Info("완료: minimal ISO와 rootfs를 준비했습니다.")
	return nil
}

// KernelArgs는 ISO에 기록된 커널 인자 목록을 반환합니다.
func (i *Installer) KernelArgs(ctx context.Context, iso string) ([]string, error) {
	out, err := execx.Output(ctx, i.Path, "iso", "kargs", "show", iso)
	if err != nil {
		return nil, fmt.Errorf("ISO 커널 인자를 확인하지 못했습니다: %s: %w", iso, err)
	}
	return strings.Fields(out), nil
}

// CustomizeOptions는 노드별 설치 ISO 생성에 필요한 값입니다.
type CustomizeOptions struct {
	SourceISO  string // full ISO 또는 minimal ISO
	Ignition   string // <hostname>.ign
	NMState    string // <hostname>.yaml
	DestDevice string // pathset의 osDisk
	OutputISO  string // <hostname>.iso
	// Force는 minimal ISO처럼 이미 customize된 ISO를 다시 다룰 때 필요한 -f 옵션입니다.
	Force bool
}

// Customize는 Ignition과 NMState를 넣은 노드별 설치 ISO를 만듭니다.
// 실패하면 부분 생성된 출력 ISO를 삭제합니다.
func (i *Installer) Customize(ctx context.Context, o CustomizeOptions) error {
	args := []string{"iso", "customize",
		"--dest-ignition", o.Ignition,
		"--dest-device", o.DestDevice,
		"--network-nmstate", o.NMState,
	}
	if o.Force {
		args = append(args, "-f")
	}
	args = append(args, "-o", o.OutputISO, o.SourceISO)

	if err := execx.Run(ctx, i.Path, args...); err != nil {
		_ = os.Remove(o.OutputISO)
		return fmt.Errorf("coreos-installer가 설치 ISO를 생성하지 못했습니다: %s: %w", o.OutputISO, err)
	}
	if !fsutil.IsNonEmptyFile(o.OutputISO) {
		_ = os.Remove(o.OutputISO)
		return fmt.Errorf("생성된 설치 ISO가 비어 있습니다: %s", o.OutputISO)
	}
	return nil
}
