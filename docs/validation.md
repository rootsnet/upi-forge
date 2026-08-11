# 사전 검증

UPI Forge는 "다른 클러스터의 노드를 부팅한다", "재구축 전 클러스터의
인증서로 설치한다" 같은 사고를 막기 위해 실행 전에 여러 검증을
수행합니다. 이 문서는 각 검증이 **무엇을 왜 검사하는지**, 실패하면
어떻게 대처하는지, 어디까지 건너뛸 수 있는지를 설명합니다.

## 한눈에 보기

| 검증 | 시점 | 건너뛰기 |
|---|---|---|
| hostname 도메인 일치 | ignition / iso / boot | 불가 |
| DNS 정방향/역방향 등록 | ignition | `--skip-dns-check` |
| 클러스터 사전 점검 (oc 로그인 대상) | prepare(cluster.version 미지정 시) / ignition | 불가 |
| 산출물 출처 — 정적 검사 | iso | 불가 |
| 산출물 출처 — MCS 인증서 대조 | iso | `--skip-ca-check` |
| ISO URL 접근 확인 | boot / live-boot | `--skip-iso-check` |

## 1. hostname 도메인 일치 (ignition · iso · boot)

nodes.csv의 hostname이 FQDN이면 도메인이 `cluster.domain`과 일치해야
합니다.

```text
오류: 노드 hostname 검증에 실패했습니다 (1건):
선택 pathset: baremetal-example
읽은 파일: /opt/upi-forge/configs/pathsets/baremetal-example/nodes.csv
노드 hostname의 도메인이 클러스터 도메인과 다릅니다: worker4.other.example.com
(cluster.domain: mycluster.example.com) — 다른 클러스터의 nodes.csv이거나
cluster.domain 설정이 잘못됐을 수 있습니다
```

**왜**: 다른 클러스터용 nodes.csv가 남아 있으면, 그 노드는 자기 클러스터
DNS에 실제로 등록되어 있어 DNS 검증까지 통과합니다. 그대로 두면 엉뚱한
클러스터로 조인하는 산출물이 만들어집니다.

**대처**: 오류에 표시된 pathset과 파일 경로를 보고, 잘못된 pathset이
선택된 것인지(`--pathset`/`pathsets.active`) CSV 내용이 오래된 것인지
확인해 바로잡습니다. 짧은 hostname(점 없는 이름)은 이 검사의 대상이
아닙니다. `cluster.apiURL`을 재정의한 경우에도 그 호스트는
`cluster.domain` 소속이어야 합니다.

## 2. DNS 등록 검증 (ignition)

hostname이 FQDN이면 그대로, 짧은 이름이면 `hostname.<cluster.domain>`으로
정방향과 역방향을 조회합니다.

- **정방향(A)**: 조회 결과에 nodes.csv의 IP가 있어야 합니다. 레코드가
  없거나, 다른 IP만 나오거나, 서버가 비정상 응답(SERVFAIL 등)이면
  실패입니다.
- **역방향(PTR)**: **잘못된 PTR이 실제로 조회된 경우에만** 실패합니다.
  PTR이 없는 것(그리고 확인할 수 없는 모든 경우)은 경고 후 통과합니다 —
  역방향 존을 두지 않는 환경을 막지 않기 위한 확정된 정책입니다.

조회는 시스템 리졸버가 아니라 **pathset의 `network.dns` 서버에 직접**
보냅니다. 노드가 실제로 사용할 서버의 등록 상태를 확인하는 것이 목적이라,
bastion의 `/etc/hosts`나 search 도메인의 영향을 받지 않습니다. 서버가
여러 개면 **응답하는 모든 서버**를 검사하며, 서버 간 결과가 다르면
오류입니다(노드는 어느 서버든 쓸 수 있으므로).

**대처**: DNS 서버에 A 레코드를 등록/수정한 뒤 다시 실행합니다. 모든
노드의 실패를 모아 한 번에 보고하므로 일괄 수정할 수 있습니다.
DNS를 아직 준비할 수 없는 검증 환경에서만 `--skip-dns-check`를 쓰세요.
`network.source: copy`이고 `network.dns`가 없으면 경고 후 자동으로
건너뜁니다.

## 3. 클러스터 사전 점검 (prepare · ignition)

현재 `oc` 로그인 대상이 설정의 클러스터(`cluster.apiURL`)와 같은지,
클러스터가 응답하는지 확인합니다. 여러 클러스터를 오가며 작업할 때
"설정은 A인데 로그인은 B" 상태를 잡아냅니다.

**대처**: `oc login`으로 올바른 클러스터에 로그인하거나
`UPI_FORGE_CONFIG`를 올바른 설정으로 바꿉니다.

## 4. 산출물 출처 검증 (iso)

기존 `<hostname>.ign`을 재사용하기 전에 그 산출물이 **지금 이 노드,
이 클러스터용이 맞는지** 확인합니다.

**정적 검사** (항상 수행, 건너뛸 수 없음):

- Ignition 안의 `/etc/hostname` 값이 대상 노드와 일치
- Ignition 안의 MCS 주소가 현재 설정과 일치
- 구조가 이 도구의 산출물 형태와 일치(변조·수작업 편집 감지)

**MCS 인증서 대조**:

현재 클러스터의 MCS에서 인증서를 받아 Ignition에 든 인증서와 CA 기준으로
대조합니다. **같은 도메인으로 클러스터를 재구축한 경우** 이름과 주소는
같아도 인증서가 달라지므로, 이 대조가 오래된 산출물을 잡아냅니다.

```text
오류: 기존 Ignition의 MCS CA가 현재 클러스터와 다릅니다: ... —
클러스터가 재구축되었을 수 있습니다. 기존 산출물(<hostname>.ign/.yaml/.iso)을
삭제하고 `upi-forge ignition`부터 다시 실행하세요
```

**대처**: 안내대로 해당 노드의 기존 산출물을 삭제하고 `ignition`부터
다시 만듭니다. iso 단계에서 MCS에 접근할 수 없는 환경이라면
`--skip-ca-check`로 **인증서 대조만** 건너뛸 수 있습니다(정적 검사는
그대로 수행됩니다).

## 5. ISO URL 접근 확인 (boot · live-boot)

부팅 전에 게시된 ISO URL의 앞부분을 GET(Range) 요청으로 읽어 접근
가능한지 확인합니다(HTTP 200/206 허용). 확인이 환경 특성으로 실패하면
`--skip-iso-check`로 건너뜁니다
(iDRAC이 실제로 받을 수 있는지는 결국 부팅 시점에 판명됩니다).

## 덮어쓰기 금지

검증과 별개로, 이미 존재하는 산출물은 덮어쓰지 않습니다.

- `<hostname>.ign`, `<hostname>.iso`가 있으면 중단 — 다시 만들려면 직접
  삭제 후 실행
- `<hostname>.yaml`(NMState)은 **내용이 같을 때만** 재사용, 다르면 중단

"이미 있습니다" 오류는 결함이 아니라, 산출물 교체를 항상 운영자의 명시적
행동으로 만들기 위한 규칙입니다.
