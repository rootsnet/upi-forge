# 설치 전 하드웨어 조사

pathset 설정의 NIC 이름과 디스크 by-path는 **운영체제가 실제로 부여하는
값**이어야 합니다. 펌웨어 화면이나 문서상의 이름과 다를 수 있으므로, 새
하드웨어 그룹은 작업 전체에서 **가장 먼저** 실측하고, 그 값으로
`pathset.yaml`과 `nics.csv`를 작성합니다.

방법은 "기본 이미지(공용 `coreos-x86_64.iso`)로 부팅해서 확인"입니다.
설치가 아니라 조사이므로 노드별 산출물(`<hostname>.ign`, `<hostname>.iso`)이
필요 없고, pathset의 미확정 값(osDisk, NIC)은 임시값이어도 됩니다.

## 베어메탈 (iDRAC10)

### 1. inventory — iDRAC 참고 정보 수집

```bash
upi-forge inventory
```

iDRAC이 보고하는 NIC(MAC, 링크 상태, 속도)과 스토리지(컨트롤러, PCI
주소)를 조회해 저장합니다. **조회(GET)만 수행**하며 서버 설정을 바꾸지
않습니다.

출력: `<pathset>/inventory/<hostname>/nic-inventory.{yaml,json}`,
`storage-inventory.{yaml,json}`

두 문서 모두 스키마 버전을 포함합니다(`schema_version` / `SchemaVersion`).
스토리지 인벤토리의 키는 장비 중립 이름을 사용합니다(`BmcAddress`,
`SlotType`, `ControllerId`).

일부 카드는 BMC가 광고한 포트 링크가 실제로는 없어서(HTTP 404) 포트 조회
경고가 나올 수 있습니다. 수집은 계속되며, 해당 포트는 장치 기능 값과
(있다면) EthernetInterfaces 보충 값으로 채워집니다. 보충 정보가 없으면
일부 값이 비어 있을 수 있습니다 —
[troubleshooting.md](troubleshooting.md#inventory) 참고.

주의: 스토리지의 by-path 값은 PCI 주소까지만 확정된 **예상값**입니다.
접미사(`-nvme-1` 등)는 커널이 붙이므로 반드시 라이브 부팅으로 확인하세요.

### 2. live-boot — 기본 이미지로 부팅해 실측

`prepare`로 만든 full ISO를 웹 서버에 게시한 뒤:

```bash
upi-forge live-boot
upi-forge --pathset <이름> live-boot    # active가 아닌 그룹을 조사할 때
```

각 노드를 iDRAC Virtual Media로 공용 ISO에서 부팅합니다. iDRAC Virtual
Console로 접속해(이머전시 모드까지만 진입해도 됩니다) 다음을 확인합니다.

```bash
ip -br link                 # NIC 이름과 링크 상태
ls -l /sys/class/net/       # NIC ↔ PCI 주소 대응
ls -l /dev/disk/by-path/    # 디스크 by-path (osDisk, Butane 대상)
```

inventory의 MAC 주소와 대조하면 어떤 이름이 어떤 물리 포트인지 확정할 수
있습니다.

### 3. 설정 반영

실측값으로 `pathset.yaml`(osDisk, activeNIC/bond)과 `nics.csv`를 작성한
뒤 `ignition` 단계로 진행합니다. 조사에 사용한 노드는 아직 아무것도
설치되지 않았으므로 그대로 두면 됩니다.

## VM

`live-boot`는 iDRAC 전용이므로 VM에는 해당되지 않습니다. 하이퍼바이저에서
기본 이미지를 가상 CD/DVD로 마운트해 부팅한 뒤, 위와 같은 명령으로
확인합니다. VM은 같은 템플릿에서 복제하면 구성이 동일하므로 한 대만
확인해도 충분한 경우가 많습니다.

## 전체 순서 요약

```text
prepare → full ISO 게시 → [베어메탈: inventory] → 기본 이미지 부팅으로 실측
        → pathset.yaml/nics.csv 작성 → ignition → iso → boot(또는 수동 마운트)
```
