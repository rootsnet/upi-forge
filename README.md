# UPI Forge

OpenShift 클러스터에 **UPI 방식으로 worker 노드를 추가**하는 작업을
자동화하는 도구입니다. RHCOS 이미지 준비부터 노드별 Ignition/NMState 생성,
노드별 설치 ISO 생성, iDRAC10 Redfish Virtual Media 부팅까지 단일 실행
파일 하나로 처리합니다.

```text
prepare → ignition → iso → (웹 서버 게시) → boot
```

- **폐쇄망(disconnected) 환경 우선**: 외부 라이브러리 의존이 없어 인터넷이
  없는 bastion에서도 소스만으로 빌드됩니다. 런타임에도 `oc`, `butane`,
  `coreos-installer` 외의 외부 명령이 필요 없습니다.
- **실행 전 사고 방지 검증**: 노드 hostname의 클러스터 도메인 일치, DNS
  정방향/역방향 등록, 기존 산출물의 출처(어느 노드·어느 클러스터용인지)를
  실행 전에 검증해, 다른 클러스터의 노드를 부팅하거나 오래된 인증서로
  설치하는 사고를 막습니다.
- **VM과 베어메탈 공용**: 같은 흐름으로 VM(수동 ISO 마운트)과
  베어메탈(iDRAC10 자동 부팅)을 모두 지원합니다.

> 이 저장소의 예시 도메인(`mycluster.example.com`)과 IP(`192.0.2.x`,
> `203.0.113.x` — 문서용 예약 대역)는 모두 자리표시자입니다. 환경에 맞게
> 바꿔 사용하세요.

## 요구 사항

| 항목 | 설명 |
|---|---|
| Go | 1.23 이상 (빌드 시에만 필요) |
| `oc` | release 이미지 조회와 추출, 클러스터 사전 점검 |
| `butane` | pathset에 `.bu` 파일이 있을 때만 필요 |
| `coreos-installer` | `upi-forge prepare`가 release 이미지에서 자동으로 준비 |
| 웹 서버 | 생성한 ISO와 rootfs를 iDRAC에 제공 (베어메탈/rootfs 모드) |
| iDRAC | **iDRAC10만 지원** (테스트 기준: PowerEdge R670). iDRAC9 이하는 Redfish 경로가 달라 동작을 보장하지 않습니다. pathset의 `bmc.type`으로 종류를 지정하며 기본값이 `idrac10`입니다 |

`jq`, `openssl`, `curl`, `python3` 등은 필요하지 않습니다.
실행 환경은 Linux를 기준으로 합니다(Windows/macOS는 빌드·개발만 지원).

## 설치

[Releases](../../releases)에서 미리 빌드된 linux-amd64 실행 파일을 받을 수
있습니다. tar.gz 안에 실행 파일과 예시 `configs/`, 이 README가 들어
있습니다(`SHA256SUMS`로 무결성 확인). 직접 빌드하려면 아래를 따르세요.

## 빌드

```bash
make
```

검사와 테스트를 거쳐 `bin/upi-forge`가 만들어집니다.

필요할 때만 쓰는 타깃: `make build`(검사 생략, 빌드만),
`make dist`(실행 파일 + configs 배포용 tar.gz), `make help`(전체 목록).

폐쇄망(disconnected) 환경에 반입할 때에도 저장소를 복사한 뒤
`go build ./cmd/upi-forge` 한 번이면
됩니다. 모듈 다운로드 단계가 없습니다.

## 빠른 시작

```bash
# 1. 설정 준비: configs/ 를 복사해 도메인·IP·pathset 값을 채웁니다.
#    (docs/configuration.md 참고)

# 2. 설정 경로를 환경 변수로 지정하고 점검 (외부 접속 없음)
export UPI_FORGE_CONFIG=/opt/upi-forge/configs/upi-forge.yaml
upi-forge config

# 3. 산출물이 쌓일 작업 디렉터리에서 이미지·도구 준비
cd /opt/upi-work
upi-forge prepare

# 4. (베어메탈) 하드웨어 실측 → pathset.yaml/nics.csv 작성
upi-forge inventory        # iDRAC 참고 정보
upi-forge live-boot        # 기본 이미지로 부팅해 NIC/디스크 실측

# 5. 노드별 Ignition/NMState 생성 (DNS·hostname 검증 포함)
upi-forge ignition

# 6. 노드별 설치 ISO 생성 (기존 산출물 출처 검증 포함)
upi-forge iso

# 7. 게시 후 부팅
install -m 0644 ./*.iso /var/www/html/iso/
upi-forge boot             # 베어메탈: iDRAC Virtual Media 부팅
                           # VM: 생성된 ISO를 가상 CD/DVD로 마운트

# 8. 설치 후 가상 미디어 정리
upi-forge eject
```

환경 변수 대신 명령마다 `--config <경로>`를 지정해도 됩니다.

## 문서

| 문서 | 내용 |
|---|---|
| [docs/getting-started.md](docs/getting-started.md) | 처음부터 끝까지 전체 워크플로 |
| [docs/configuration.md](docs/configuration.md) | 설정 파일과 CSV 레퍼런스 |
| [docs/hardware-survey.md](docs/hardware-survey.md) | 설치 전 하드웨어 조사 (inventory / live-boot) |
| [docs/validation.md](docs/validation.md) | 사전 검증의 동작과 건너뛰기 옵션 |
| [docs/troubleshooting.md](docs/troubleshooting.md) | 자주 만나는 오류와 대처 |

## 명령 요약

| 명령 | 역할 |
|---|---|
| `upi-forge config` | 읽어 들인 설정과 계산된 경로 점검 |
| `upi-forge prepare` | RHCOS ISO/coreos-installer 준비, minimal ISO·rootfs 분리 |
| `upi-forge ignition` | 노드별 NMState YAML과 Ignition 생성 |
| `upi-forge iso [--full\|--rootfs] [NODE ...]` | 노드별 설치 ISO 생성 |
| `upi-forge boot [--from HOST] [NODE ...]` | iDRAC Virtual Media로 설치 부팅 |
| `upi-forge inventory [--from HOST] [NODE ...]` | iDRAC NIC/스토리지 인벤토리 수집 |
| `upi-forge live-boot [--from HOST] [NODE ...]` | 공용 ISO로 라이브 부팅(하드웨어 실측용) |
| `upi-forge eject [--from HOST] [NODE ...]` | 가상 미디어 분리 및 정리 |
| `upi-forge version` | 빌드 버전 확인 |

각 명령은 `upi-forge <명령> -h`로 상세 도움말을 제공합니다.
BMC 명령(boot/live-boot/eject/inventory)은 NODE 인자로 특정 노드만
처리할 수 있고, pathset의 `bmc.samePassword`(공통 비밀번호 1회 입력)와
`bmc.parallel`(노드 동시 처리)을 지원합니다 —
[docs/configuration.md](docs/configuration.md) 참고.
`--from HOST`와 `NODE ...`는 서로 배타적이며 함께 사용할 수 없습니다.
전역 옵션 `--debug`를 켜면 실행되는 외부 명령과 Redfish 요청이 그대로
출력됩니다(토큰·비밀번호 제외) —
[docs/troubleshooting.md](docs/troubleshooting.md) 참고.

## 안전 규칙

운영 사고와 직결되는 규칙은 도구가 강제합니다.

- 기존 Ignition과 노드별 ISO는 **덮어쓰지 않습니다** (재사용은 내용이 같을 때만).
- 노드 hostname이 FQDN이면 도메인이 `cluster.domain`과 일치해야 합니다
  — 다른 클러스터의 nodes.csv를 실수로 쓰는 사고를 막습니다.
- `nodes.csv`와 `idracs.csv`의 hostname 일대일 대응을 검사합니다
  — 다른 장비를 부팅하는 사고를 막습니다.
- 기존 Ignition은 hostname·MCS 주소·MCS 인증서까지 현재 클러스터와
  대조합니다 — 재구축된 클러스터의 오래된 산출물을 걸러냅니다.
- 부트 순서에서 Virtual CD/DVD를 제외해 설치 후 무한 재부팅을 막습니다.
- iDRAC 비밀번호는 파일에 저장하지 않고 실행 시 입력받습니다
  (자동화는 환경 변수 `UPI_FORGE_IDRAC_PASSWORD` 사용).

자세한 동작은 [docs/validation.md](docs/validation.md)를 보세요.

## 라이선스

Apache License 2.0 — [LICENSE](LICENSE) 참고.
