package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"upi-forge/internal/bmc"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

const inventoryUsage = `upi-forge inventory - iDRAC에서 NIC와 스토리지 인벤토리를 수집합니다.

기존 pathset-3/00_preparation_idrac10_inventory.sh에 해당합니다.

베어메탈 전용 참고 정보 수집 단계입니다. iDRAC이 보고하는 NIC(MAC, 링크,
속도)와 스토리지(컨트롤러, PCI 주소)를 미리 확인한 뒤, 실제 NIC 이름과
디스크 by-path의 최종 확정은 라이브 부팅(upi-forge live-boot)으로 합니다.
VM 대상에는 iDRAC이 없으므로 이 단계 없이 기본 이미지 부팅으로만 확인합니다.

조회(GET)만 수행하며 서버 전원, BIOS, NIC 또는 RAID 설정을 변경하지 않습니다.

사용법:
  upi-forge inventory [--from HOSTNAME] [NODE ...]

NODE를 지정하면 그 노드만 처리합니다(병렬 실행에서 실패한 노드만 다시
실행할 때 사용). --from과 NODE는 함께 사용할 수 없습니다.
pathset의 bmc.samePassword와 bmc.parallel 동작은 boot와 같습니다
(upi-forge boot -h 참고).

출력:
  <pathset>/inventory/<hostname>/nic-inventory.yaml
  <pathset>/inventory/<hostname>/nic-inventory.json
  <pathset>/inventory/<hostname>/storage-inventory.yaml
  <pathset>/inventory/<hostname>/storage-inventory.json

조사 대상 pathset은 전역 옵션 --pathset으로 지정할 수 있습니다.

주의:
  스토리지 인벤토리의 by-path 값은 PCI 주소까지만 확정된 예상값입니다.
  접미사(-nvme-1 등)는 커널이 붙이므로 라이브 부팅 후
  ls -l /dev/disk/by-path/ 로 최종 확인한 뒤 pathset의 disk.osDisk에 사용하세요.`

func runInventory(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("inventory", inventoryUsage)
	from := fs.String("from", "", "지정한 hostname부터 이어서 처리합니다")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}

	cfg, ps, err := app.Pathset()
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

	outputRoot := ps.InventoryDir()
	logx.KeyValues(
		"선택 pathset", ps.Name,
		"결과 디렉터리", outputRoot,
		"대상 노드 수", fmt.Sprint(len(targets)),
	)

	err = forEachBMCNode(ctx, app, cfg, ps, idracs, targets, []string{"inventory"}, fs.NArg() > 0,
		func(host string, _ csvdata.IDRAC) {
			logx.Info("수집 결과 저장 위치: %s", filepath.Join(outputRoot, host))
		},
		func(ctx context.Context, host string, _ csvdata.IDRAC, client *redfish.Client) error {
			dir := filepath.Join(outputRoot, host)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("결과 디렉터리를 만들지 못했습니다: %s: %w", dir, err)
			}

			logx.Info("[1/2] NIC 인벤토리 수집")
			nic, err := driver.NICInventory(ctx, client)
			if err != nil {
				return err
			}
			if err := writeNICInventory(dir, nic); err != nil {
				return err
			}
			logx.Info("NIC 카드 수: %d, NIC 포트 수: %d", len(nic.Adapters), nic.PortCount())
			for _, adapter := range nic.Adapters {
				for _, port := range adapter.Ports {
					logx.Info("  %-20s %-18s %-8s %s Mbps",
						port.ID, port.PermanentMACAddress, port.LinkStatus, formatInt(port.CurrentSpeedMbps))
				}
			}

			logx.Info("[2/2] 스토리지 인벤토리 수집")
			storage, err := driver.StorageInventory(ctx, client)
			if err != nil {
				return err
			}
			if err := writeStorageInventory(dir, storage); err != nil {
				return err
			}
			logx.Info("설치 대상(dest-device) 후보:")
			for _, line := range storage.DestDeviceCandidates() {
				logx.Info("%s", line)
			}
			return nil
		})
	if err != nil {
		return err
	}

	logx.Blank()
	logx.Info("완료: 모든 노드의 NIC와 스토리지 인벤토리 수집이 끝났습니다.")
	logx.Info("주의: by-path 예상값의 접미사는 라이브 부팅으로 반드시 확인하세요.")
	return nil
}

// writeNICInventory는 같은 구조체를 YAML과 JSON으로 저장합니다.
// 원본에서 이 변환에 필요했던 python3와 PyYAML은 더 이상 필요하지 않습니다.
func writeNICInventory(dir string, inv *bmc.NICInventory) error {
	base := filepath.Join(dir, "nic-inventory")
	if err := fsutil.WriteFileAtomic(base+".yaml", inv.MarshalYAML(), 0o644); err != nil {
		return err
	}
	if err := writeJSON(base+".json", inv); err != nil {
		return err
	}
	logx.Info("저장: %s.yaml, %s.json", base, base)
	return nil
}

// writeStorageInventory는 NIC 인벤토리와 같은 방식으로 YAML과 JSON을
// 함께 저장합니다.
func writeStorageInventory(dir string, inv *bmc.StorageInventory) error {
	base := filepath.Join(dir, "storage-inventory")
	if err := fsutil.WriteFileAtomic(base+".yaml", inv.MarshalYAML(), 0o644); err != nil {
		return err
	}
	if err := writeJSON(base+".json", inv); err != nil {
		return err
	}
	logx.Info("저장: %s.yaml, %s.json", base, base)
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 직렬화에 실패했습니다: %w", err)
	}
	data = append(data, '\n')
	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return err
	}
	return nil
}

func formatInt(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(*v)
}
