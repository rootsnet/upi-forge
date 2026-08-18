# 설정 레퍼런스

설정은 두 층입니다.

- `upi-forge.yaml` — 클러스터·레지스트리·웹 서버 등 **공통 설정**
- `pathsets/<이름>/` — 하드웨어 그룹별 디스크·네트워크·노드 목록

알 수 없는 키는 줄 번호와 함께 즉시 오류로 알려주므로, 오타가 조용히
무시되는 일은 없습니다.

## 경로 해석 규칙

- 설정 파일 안의 상대 경로 → **설정 파일이 있는 디렉터리** 기준
- `workspace.dir` → **현재 작업 디렉터리** 기준 (비우면 실행한 곳)

설정 묶음은 한곳(예: `/opt/upi-forge/configs/`)에 두고, 산출물은 작업
디렉터리에 쌓는 구성을 권합니다.

## upi-forge.yaml

```yaml
cluster:
  domain: "mycluster.example.com"   # worker가 조인할 클러스터 도메인
  apiURL: ""                        # 비우면 https://api.<domain>:6443
  version: ""                       # 비우면 로그인한 클러스터에서 조회
  mcsPort: 22623
```

| 키 | 설명 |
|---|---|
| `cluster.domain` | 모든 검증과 MCS 주소의 기준입니다. 노드 FQDN은 이 도메인 소속이어야 합니다. |
| `cluster.apiURL` | 지정 시에도 호스트가 `cluster.domain` 소속이어야 합니다(다른 클러스터를 가리키는 재정의 차단). |
| `registry.pullSecret` | release 이미지용 pull secret 경로. **저장소에 커밋하지 마세요.** |
| `registry.registry` | 연결 환경에서는 `quay.io`, 폐쇄망(disconnected) 환경에서는 mirror registry (예: `mirror.example.com/ocp4`) |
| `webServer.url`, `webServer.path` | 생성한 ISO/rootfs를 게시할 웹 서버. iDRAC과 노드에서 접근 가능해야 합니다. `http`/`https`만 허용. |
| `workspace.*` | 산출물 파일 이름들. 기본값 그대로 쓰면 됩니다. `tmpDir`는 prepare가 통째로 삭제하므로 작업 디렉터리 안이어야 합니다. |
| `pathsets.active` | 현재 작업할 하드웨어 그룹. `--pathset` 전역 옵션으로 일시 변경 가능. |
| `redfish.*` | iDRAC 연결 옵션. 자체 서명 인증서 환경은 `tlsVerify: false`. `*WaitSeconds`는 전원/미디어 조작 사이 대기 시간. `powerOffWaitSeconds`는 전원 꺼짐 확인 폴링의 최대 대기로, 꺼짐이 확인되면 즉시 진행하고 시간 안에 확인되지 않아도 경고 후 진행합니다. |
| `tools.*` | 외부 명령 경로. `coreosInstaller`를 비우면 prepare가 추출한 것을 사용합니다. |
| `logger.debug` | `true`면 상세 로그 — 실행되는 외부 명령(인자 포함)과 Redfish 요청이 그대로 출력됩니다. 전역 옵션 `--debug`로 일시적으로 켤 수도 있습니다. |

## pathset.yaml

하드웨어 그룹 하나의 설정입니다. **같은 그룹의 노드는 디스크 by-path와
NIC 구성이 동일**해야 합니다. 디렉터리 이름과 `name`이 일치해야 합니다.

```yaml
name: "baremetal-example"
description: "베어메탈 / iDRAC10 / NIC 본딩 / NVMe RAID1"

iso:
  useRootfs: false        # 기본 ISO 모드 (--full/--rootfs로 일시 변경)

disk:
  osDisk: "/dev/disk/by-path/pci-0000:3c:00.0-nvme-1"   # 실측값

butane: "baremetal-example.bu"   # 선택: 추가 디스크/RAID 등

network:
  source: "generate"      # generate | copy
  gateway: "192.0.2.1"
  prefixLength: 24
  dns: ["192.0.2.10", "192.0.2.11"]
  bonding: true
  bond:
    name: "bond0"
    mode: "active-backup"
    primary: "eno1np0"
    standby: "eno2np1"
    miimon: 100

bmc:
  type: "idrac10"           # 생략 시 idrac10 (현재 유일한 지원 종류)
  samePassword: false       # true면 공통 비밀번호를 한 번만 입력
  parallel: 0               # 2 이상이면 그 개수만큼 노드 동시 처리
```

| 키 | 설명 |
|---|---|
| `disk.osDisk` | OS가 설치될 디스크의 by-path. **Butane의 wipe/RAID 대상에 절대 포함 금지.** 접미사(`-nvme-1` 등)까지 라이브 부팅으로 실측하세요. |
| `butane` | 지정하면 `butane`으로 렌더해 worker Ignition에 병합합니다. 병합 지원 범위는 `storage.{disks,raid,filesystems,files,directories,links,luks}`와 `systemd.units`이며, 그 밖의 필드(`passwd.users`, `kernelArguments` 등)가 있으면 조용히 누락되는 대신 오류로 중단합니다. 템플릿과 같은 파일 경로(`/etc/hostname` 등)를 정의해도 생성 단계에서 거부됩니다. |
| `network.source` | `generate`: nodes.csv/nics.csv와 위 값으로 NMState 생성(IPv4 고정 IP). `copy`: 미리 만든 `network-yaml/<hostname>.yaml`을 그대로 사용(IPv6 등 특수 구성용). |
| `network.dns` | 노드가 사용할 DNS 서버. ignition의 DNS 검증도 이 서버들로 직접 조회합니다. copy 모드에서는 생략 가능(생략 시 DNS 검증은 건너뜀). |
| `network.bonding` | `true`면 `bond.*` 두 NIC로 active-backup 본딩, `false`면 `activeNIC` 하나만 사용. |
| `bmc.type` | idracs.csv의 관리 컨트롤러 종류. `boot`/`live-boot`/`eject`/`inventory`가 이 값으로 구현을 선택합니다. 현재 `idrac10`만 지원하며 생략 시 기본값입니다. |
| `bmc.samePassword` | `true`면 모든 노드가 같은 BMC 비밀번호를 쓴다고 보고, 대상 노드와 작업 내용을 모두 보여준 뒤 한 번만 입력받습니다. `parallel`과 독립적이라 순차 실행에도 그대로 적용됩니다(한 대씩 처리하되 입력은 한 번). 노드별 환경 변수(`UPI_FORGE_IDRAC_PASSWORD_<HOSTNAME>`)는 이때 무시됩니다. 생략 시 `false`(노드별 입력). |
| `bmc.parallel` | BMC 명령의 동시 실행 노드 수 **상한**. `0`/`1`(기본)은 순차 처리, `2` 이상이면 비밀번호를 모두 받은 뒤 그 개수만큼 동시에 처리합니다. 대상이 상한보다 적으면 대상 수만큼만 동시에 돌고(3대 + `parallel: 4` → 3대 동시), 한 대가 끝날 때마다 대기 중인 다음 노드가 이어집니다. 대상이 1대뿐이면 값과 무관하게 순차로 실행됩니다. 병렬일 때 노드별 상세 출력은 pathset의 `logs/<명령>-<실행시각>-<고유값>/<hostname>.log`에 남고 화면에는 전체 진행만 표시합니다. 일부 노드가 실패해도 나머지는 끝까지 진행하며, 마지막에 실패한 노드만 다시 실행할 명령(예: `upi-forge boot worker5 worker7`)을 안내합니다. |

## CSV 파일

세 파일 모두: UTF-8 BOM·CRLF 허용, 헤더 행은 선택(쓰려면 정확한 이름),
열 개수 초과·중복·빈 값은 오류입니다. **행 순서가 처리 순서**입니다.

### nodes.csv — `hostname,ip`

```csv
hostname,ip
worker4.mycluster.example.com,192.0.2.111
worker5.mycluster.example.com,192.0.2.112
```

- hostname은 FQDN 또는 짧은 이름. **FQDN이면 도메인이 `cluster.domain`과
  일치해야** 합니다. 짧은 이름은 DNS 검증 시 도메인을 붙여 조회하지만,
  노드의 `/etc/hostname`에는 적은 값이 그대로 들어갑니다 — 기존 노드들이
  FQDN 이름을 쓴다면 FQDN으로 적는 것을 권합니다.
- ip는 노드에 고정 할당할 IPv4 주소입니다.

### nics.csv — 한 줄에 NIC 이름 하나

```csv
nic
eno1np0
eno2np1
```

실측한 NIC 이름 목록입니다. `pathset.yaml`의 `activeNIC`/`bond.primary`/
`bond.standby`는 이 목록에 있어야 합니다. 목록에 있으나 사용하지 않는
NIC은 명시적으로 비활성(down) 처리되어 NMState에 들어갑니다.

### idracs.csv — `hostname,idrac_ip,idrac_id` (베어메탈만)

```csv
hostname,idrac_ip,idrac_id
worker4.mycluster.example.com,203.0.113.111,root
worker5.mycluster.example.com,203.0.113.112,root
```

- hostname은 nodes.csv와 **일대일 대응**해야 합니다(다른 장비 부팅 방지).
- **비밀번호는 기록하지 않습니다.** 실행 시 입력받거나 환경 변수를 씁니다.
- 이 파일이 있는 pathset은 베어메탈로, 없으면 VM으로 간주합니다.

## 환경 변수

| 변수 | 용도 |
|---|---|
| `UPI_FORGE_CONFIG` | `--config` 대신 쓸 설정 파일 경로 |
| `UPI_FORGE_IDRAC_PASSWORD` | 모든 iDRAC 공통 비밀번호 |
| `UPI_FORGE_IDRAC_PASSWORD_<HOSTNAME>` | 노드별 비밀번호(공통보다 우선). hostname의 `.`·`-`를 `_`로 바꾸고 대문자화 |
