# 자주 만나는 오류와 대처

오류 메시지 자체에 확인할 항목이 안내되지만, 상황별 배경을 함께 정리합니다.
검증 관련 오류(도메인·DNS·산출물 출처)는 [validation.md](validation.md)를
먼저 보세요.

## 설정·시작 단계

**`cluster.domain이 비어 있습니다` / `알 수 없는 키` (줄 번호 포함)**
설정 파일의 필수 값 누락 또는 오타입니다. 오타는 조용히 무시되지 않고
전부 오류로 잡힙니다. `upi-forge config`로 읽힌 값을 확인하세요.

**`pathsets.active와 pathset.yaml의 name이 다릅니다`**
pathset 디렉터리 이름과 `pathset.yaml`의 `name`은 일치해야 합니다.

**`workspace.tmpDir가 작업 디렉터리 밖을 가리킵니다`**
`tmpDir`는 prepare가 디렉터리 전체를 삭제하는 경로라 작업 디렉터리 안으로
제한됩니다(심볼릭 링크로 밖을 가리키는 경우 포함). 설정을 확인하세요.

## prepare

**`oc 명령을 찾을 수 없습니다`**
`oc`가 PATH에 없거나 `tools.oc` 경로가 잘못됐습니다.

**`현재 oc 로그인 대상이 설정과 다릅니다`**
다른 클러스터에 로그인된 상태입니다. `oc login` 후 다시 실행하세요.

**release 이미지 추출 실패**
pull secret 경로(`registry.pullSecret`)와 mirror registry 주소
(`registry.registry`)를 확인하세요. 폐쇄망(disconnected) 환경에서는
mirror registry에 해당 버전의 release 이미지가 있어야 합니다.

## ignition

**`MCS에 연결할 수 없습니다: api-int.mycluster.example.com:22623`**
bastion에서 MCS(22623/tcp)에 접근할 수 있어야 인증서를 수집합니다.
방화벽과 `api-int` DNS 등록을 확인하세요.

**`Butane 렌더에 실패했습니다`**
pathset의 `.bu` 문법 오류이거나 `butane` 버전이 스펙과 맞지 않습니다.
`butane --strict <파일>`로 직접 확인해 보세요.

**`storage.files에 중복 path가 있습니다`**
pathset Butane이 템플릿과 같은 파일(예: `/etc/hostname`)을 정의했습니다.
Butane에서 해당 항목을 제거하세요.

**`이미 있습니다` (기존 .ign/.yaml)**
덮어쓰기 금지 규칙입니다. 다시 만들려는 노드의 산출물을 직접 삭제한 뒤
실행하세요. 부분 실패 후 재실행할 때도 같습니다 — 이미 정상 생성된
노드의 파일을 지우거나, 남길 노드만 새 pathset으로 나누세요.

## iso

**`full ISO가 없습니다. 먼저 upi-forge prepare를 실행하세요`**
작업 디렉터리가 다르거나 prepare를 건너뛴 경우입니다. 산출물이 있는
디렉터리에서 실행하는지 확인하세요.

**`rootfs 분리 모드에 필요한 rootfs URL이 비어 있습니다`**
rootfs 모드는 웹 서버 설정(`webServer`)이 필수입니다(VM 대상이어도).

**기존 Ignition 출처 오류 (`hostname이 대상 노드와 다릅니다` 등)**
[validation.md](validation.md)의 산출물 출처 검증 절을 보세요. 원칙은
"해당 노드의 기존 산출물 삭제 → `ignition`부터 재생성"입니다.

## inventory

**`BMC가 광고한 포트 리소스가 없습니다(HTTP 404)` 경고**
일부 NIC 카드나 펌웨어는 장치 기능에서 광고한 포트 리소스를 실제로
제공하지 않아 HTTP 404를 반환합니다. 이 경우 수집은 계속됩니다: 해당
포트는 장치 기능 값을 사용하고, 대응하는 EthernetInterfaces 항목이
있으면 링크 상태와 속도를 보충합니다. 보충 정보를 찾지 못한 항목은
일부 값이 비어 있을 수 있으므로, 최종 NIC 정보는 원래 절차대로
`live-boot` 실측으로 확정하세요. 404 이외의 오류(인증·권한·서버 오류)는
실제 문제로 보고 수집을 중단합니다.

## boot / live-boot / eject

**`ISO URL 사전 접근 확인 실패`**
웹 서버 주소·방화벽을 확인하세요. iDRAC 관리망에서 웹 서버에 접근
가능해야 합니다(GET Range 요청, HTTP 200/206). 환경 특성으로 확인이
어려우면 `--skip-iso-check`.

**`Redfish 세션 생성 실패 (HTTP 401/403)`**
iDRAC 계정·비밀번호·계정 잠금·Login 권한을 확인하세요. 비밀번호 환경
변수(`UPI_FORGE_IDRAC_PASSWORD*`)를 쓰는 경우 변수 이름 규칙(대문자,
`.`→`_`)을 확인하세요.

**`HTTP 429` (세션 수 제한)**
iDRAC의 활성 세션이 가득 찼습니다. iDRAC 웹 UI에서 미사용 세션을
정리하세요. UPI Forge는 정상 종료, 오류 발생, Ctrl+C 중단 시 자기 세션
삭제를 시도하고, 삭제가 확인되지 않으면 경고합니다. 이 경고가 나오거나
세션 수가 줄지 않으면 BMC의 활성 세션을 직접 확인하세요. 다른 도구가
남긴 세션은 UPI Forge에서 확인할 수 없습니다.

**`Virtual Media 장치 목록을 조회하지 못했습니다 … (HTTP 404)`** (iDRAC9)
`eject --address`로 iDRAC9 장비를 지정하면서 `--bmc-type idrac9`를 생략하면
iDRAC10 경로를 조회해 404가 납니다. CSV 경로에서는 pathset의 `bmc.type`을
확인하세요. iDRAC9 드라이버는 System이 광고한 경로 → `Systems` 표준 경로
→ `Managers` 구형 경로 순으로 404일 때만 다음 후보를 시도하며, 그 밖의
오류(인증·서버 오류)는 실제 문제로 보고 중단합니다.

**`SCP Import 작업이 … 끝나지 않았습니다` / `… 실패했습니다`** (iDRAC9)
iDRAC9의 원타임 부트 설정은 SCP Import 작업으로 이루어집니다. 도구는
작업 상태가 `Completed`이고, `PercentComplete`를 제공하는 펌웨어에서는
진행률이 100%가 된 뒤에만 전원을 켭니다. 작업이 실패
(`Exception`, `CompletedWithErrors`, `TaskStatus=Critical` 등)하거나
5분 안에 끝나지 않으면 전원을 켜지 않고 중단합니다. 메시지에 나온 작업
경로(`/redfish/v1/TaskService/Tasks/JID_…`)를 iDRAC Job Queue에서 확인해
정리한 뒤 해당 노드만 다시 실행하세요(`upi-forge boot <hostname>`).
중단 시점에 서버는 꺼진 상태로 남으며, 재실행은 꺼진 서버에 전원 끄기를
다시 보내지 않으므로 그대로 이어서 실행하면 됩니다.
작업 조회가 401/403이면 iDRAC 계정 권한을 확인하세요(SCP Import에는
Configure 권한이 필요합니다).

**`부트 설정을 확인하지 못했습니다`** (iDRAC9)
작업 위치를 알 수 없는 펌웨어에서 결과 속성(DellAttributes)을 읽어
확인하는 경로가 인증·서버 오류나 응답 지연으로 실패한 경우입니다.
`redfish.attributeWaitSeconds`를 늘리거나 iDRAC 상태를 확인한 뒤 다시
실행하세요. 확인 경로 자체가 없는 구형 펌웨어(HTTP 404)는 경고만 남기고
진행하므로, 이때는 iDRAC Virtual Console에서 ISO 부팅을 직접 확인하세요.

**`부트 순서 Settings 리소스가 없어 … 건너뜁니다`** (iDRAC9)
평상시 부트 순서에서 가상 CD/DVD를 빼는 보호 단계를 이 펌웨어에서는
수행할 수 없어 건너뛴 것입니다. 설치는 원타임 부트로 정상 진행되며,
설치 후 `upi-forge eject`와 BIOS 부트 순서 확인이 ISO 재부팅의
방어선입니다.

**중간 노드에서 실패**
실패 시점에 `--from <hostname>` 재개 명령이 안내됩니다. 원래 실행에
쓴 옵션(`--skip-iso-check` 등)도 안내에 포함됩니다.

**병렬 실행(`bmc.parallel`)에서 일부 노드 실패**
나머지 노드는 끝까지 진행되고, 마지막에 실패 노드 목록과 그 노드만
다시 실행하는 명령(예: `upi-forge boot worker5 worker7`)이 안내됩니다.
원인은 pathset의 `logs/<명령>-<실행시각>-<고유값>/<hostname>.log`에서
확인하세요. 순차 실행에서 화면에 표시되는 노드별 출력 전체가
들어 있습니다(`--debug`를 켰다면 상세도 포함). 병렬에서는 실패가
순서와 무관하게 흩어지므로 `--from`이 아니라 NODE 인자로 재시도합니다.

**부트 순서 관련 경고가 나오지만 계속 진행됨**
의도된 동작입니다. 부트 순서 표현은 장비·펌웨어마다 달라, 이 보호 단계가
실패해도 설치는 원타임 부트로 계속합니다. 설치 후 `eject`와 부트 설정
확인이 무한 재부팅의 최종 방어선입니다.

**설치가 끝났는데 노드가 다시 설치 화면으로 부팅함**
가상 미디어가 아직 연결되어 있습니다. `upi-forge eject`를 실행하세요.
VM은 하이퍼바이저에서 가상 CD/DVD를 분리하세요.

## 설치 후

**노드가 클러스터에 나타나지 않음**
UPI 공통 절차인 CSR 승인이 필요합니다.

```bash
oc get csr | grep Pending
oc adm certificate approve <csr-name>   # 보통 노드당 2회
```

**로그를 더 보고 싶을 때 — `--debug`**
전역 옵션 `--debug`(또는 설정의 `logger.debug: true`)를 켜면 도구가
실행하는 명령과 Redfish 요청을 확인할 수 있습니다.

- 모든 외부 명령을 실행 직전에 인자까지 출력: `debug: 실행: oc adm release extract ...`,
  `debug: 실행: coreos-installer iso customize ...`
- BMC로 보내는 모든 Redfish 요청: `debug: POST https://<주소>/redfish/v1/...`,
  세션 생성/삭제 완료
- 외부 명령의 stderr 중계: `debug: <명령> stderr: ...`

```bash
upi-forge --debug boot
```

동작이 예상과 다를 때 어떤 명령·요청이 나갔는지 이 로그로 대조할 수
있습니다. 토큰과 비밀번호, Redfish 요청 본문은 debug에서도 로그에 남지
않습니다.
