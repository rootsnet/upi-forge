# 전체 워크플로

UPI Forge로 OpenShift 클러스터에 worker 노드를 추가하는 전 과정입니다.
예시의 도메인·IP는 자리표시자이므로 환경에 맞게 바꿔 읽으세요.

- 클러스터 도메인: `mycluster.example.com`
- 노드망: `192.0.2.0/24` (게이트웨이 `.1`, DNS `.10`/`.11`)
- iDRAC 관리망: `203.0.113.0/24`
- ISO 게시 웹 서버: `http://192.0.2.50:8080/iso`

## 0. 사전 준비

- bastion에서 대상 클러스터에 `oc` 로그인이 되어 있어야 합니다
  (`oc whoami --show-server`가 `https://api.mycluster.example.com:6443`).
- 추가할 노드의 **DNS 정방향(A) 레코드**를 미리 등록합니다.
  역방향(PTR)은 선택이지만, 등록한다면 정방향과 일치해야 합니다.
- release 이미지를 받을 pull secret 파일을 준비합니다.

## 1. 설정 작성

`configs/`를 원하는 위치(예: `/opt/upi-forge/configs/`)로 복사한 뒤:

1. `upi-forge.yaml` — 클러스터 도메인, 레지스트리, 웹 서버 주소를 채웁니다.
2. `pathsets/<이름>/` — 하드웨어 그룹(pathset)마다 디렉터리를 만듭니다.
   같은 pathset의 노드는 **디스크 by-path와 NIC 구성이 동일**해야 합니다.
   - `pathset.yaml` — 디스크, 네트워크(본딩 여부), 선택적 Butane
   - `nodes.csv` — `hostname,ip`
   - `nics.csv` — 사용할 NIC 이름 목록
   - `idracs.csv` — (베어메탈만) `hostname,idrac_ip,idrac_id`
3. `upi-forge.yaml`의 `pathsets.active`로 작업할 그룹을 선택합니다.
   한 번만 바꾸려면 전역 옵션 `--pathset <이름>`을 씁니다.

필드 상세는 [configuration.md](configuration.md)를 보세요.

```bash
upi-forge --config /opt/upi-forge/configs/upi-forge.yaml config
```

어떤 클러스터·pathset·경로가 사용되는지 외부 접속 없이 확인합니다.
이후 예시는 `UPI_FORGE_CONFIG` 환경 변수를 설정했다고 가정합니다.

```bash
export UPI_FORGE_CONFIG=/opt/upi-forge/configs/upi-forge.yaml
```

## 2. 이미지와 도구 준비 — `prepare`

산출물이 쌓일 작업 디렉터리에서 실행합니다.

```bash
mkdir -p /opt/upi-work && cd /opt/upi-work
upi-forge prepare
```

release 이미지에서 RHCOS full ISO와 `coreos-installer`를 추출하고,
minimal ISO와 rootfs 이미지를 분리합니다.

생성물: `coreos-x86_64.iso`, `coreos-installer`,
`coreos-x86_64-minimal.iso`, `coreos-x86_64-rootfs.img`

- `cluster.version`이 비어 있으면 로그인한 클러스터의 버전을 자동 조회합니다.
- full ISO만 쓸 거라면 `--skip-rootfs-split`로 분리를 생략할 수 있습니다.

## 3. (베어메탈) 하드웨어 실측

pathset 설정에 들어가는 NIC 이름과 디스크 by-path는 **OS가 실제로
부여하는 값**이어야 합니다. 새 하드웨어 그룹이라면 먼저 실측하세요 —
절차는 [hardware-survey.md](hardware-survey.md)에 있습니다.

VM은 하이퍼바이저에서 기본 이미지(`coreos-x86_64.iso`)를 마운트해 같은
방법으로 확인합니다.

## 4. 노드별 Ignition/NMState 생성 — `ignition`

```bash
upi-forge ignition
```

pathset의 nodes.csv 전체에 대해 `<hostname>.yaml`(NMState)과
`<hostname>.ign`(Ignition)을 만듭니다. 생성 전에 다음을 자동 검증합니다.

- 노드 hostname의 클러스터 도메인 일치
- DNS 정방향/역방향 등록 ([validation.md](validation.md))
- `oc` 로그인 대상과 설정의 클러스터 일치

MCS 인증서 체인은 실행 시점에 클러스터에서 직접 읽어 Ignition에 넣으며,
파일로 따로 저장하지 않습니다. pathset에 `.bu`(Butane) 파일이 있으면
렌더해서 병합합니다(RAID, 추가 마운트 등).

기존 `<hostname>.ign`이 있으면 덮어쓰지 않고 중단합니다. 다시 만들려면
해당 파일을 직접 삭제한 뒤 실행하세요.

## 5. 노드별 설치 ISO 생성 — `iso`

```bash
upi-forge iso                                  # pathset의 iso.useRootfs 사용
upi-forge iso --rootfs                         # 이번 실행만 rootfs 분리 모드
upi-forge iso --full worker1.mycluster.example.com   # 특정 노드만
```

노드별 `<hostname>.iso`를 만듭니다. Ignition·NMState·설치 디스크 설정이
ISO 안에 들어가므로 부팅만 하면 자동 설치됩니다.

- **full 모드**: ISO 하나로 설치가 끝납니다.
- **rootfs 분리 모드**: ISO는 작지만, 부팅한 노드가 웹 서버에서
  `coreos-x86_64-rootfs.img`를 내려받아야 합니다(VM 대상이어도 웹 서버 필요).

실행 전에 기존 `<hostname>.ign`의 출처(대상 노드·클러스터·인증서)를
검증합니다 — 상세는 [validation.md](validation.md).

## 6. 게시와 부팅

### 베어메탈 (iDRAC9 / iDRAC10)

pathset의 `bmc.type`이 장비 세대를 정합니다(생략 시 `idrac10`, iDRAC9
장비는 `idrac9`). 절차는 두 세대가 같고 도구가 Redfish 경로 차이를
처리합니다 — [configuration.md](configuration.md)의 `bmc.type` 참고.

```bash
# 웹 서버에 게시
install -m 0644 ./*.iso /var/www/html/iso/
install -m 0644 coreos-x86_64-rootfs.img /var/www/html/iso/   # rootfs 모드일 때

# iDRAC Virtual Media 부팅
upi-forge boot
```

노드마다 iDRAC 비밀번호를 입력받아(화면에 표시되지 않음) 가상 미디어를
연결하고 원타임 부트로 설치를 시작합니다. 중간에 실패하면 이어서 실행할
명령(`--from <hostname>`)을 안내합니다.

```bash
upi-forge boot --from worker5.mycluster.example.com   # 실패한 노드부터 재개
upi-forge boot --skip-iso-check                       # ISO URL 사전 확인을 건너뛸 때
```

### VM

생성된 `<hostname>.iso`를 하이퍼바이저에서 대상 VM의 가상 CD/DVD로
마운트하고 부팅하면 됩니다. `iso` 명령이 pathset에 idracs.csv가 없으면
VM 대상으로 간주해 필요한 안내를 출력합니다.

## 7. 설치 후

```bash
# 가상 미디어 분리 (ISO로 재부팅되는 것 방지)
upi-forge eject
# CSV 없이 한 대만 직접 지정할 때 (iDRAC9 장비는 --bmc-type idrac9 필수)
upi-forge eject --address 203.0.113.21 --bmc-type idrac9

# 노드 조인 승인 (UPI 공통 절차)
oc get csr
oc adm certificate approve <csr-name>    # Pending CSR 승인, 보통 2회
oc get nodes
```

설치가 끝난 VM도 가상 CD/DVD를 분리해 두세요.

## 자동화 팁

- iDRAC 비밀번호를 대화형으로 입력하기 어려운 환경에서는 환경 변수를
  사용합니다: 공통 `UPI_FORGE_IDRAC_PASSWORD`, 노드별
  `UPI_FORGE_IDRAC_PASSWORD_<HOSTNAME>` (hostname의 `.`과 `-`는 `_`로,
  대문자로: `worker4.mycluster.example.com` →
  `UPI_FORGE_IDRAC_PASSWORD_WORKER4_MYCLUSTER_EXAMPLE_COM`).
- 여러 클러스터를 오갈 때는 클러스터별 설정 디렉터리를 두고
  `UPI_FORGE_CONFIG`만 바꾸는 방식을 권합니다. 잘못된 조합은 사전 검증이
  차단하지만, 처음부터 섞이지 않는 편이 좋습니다.
