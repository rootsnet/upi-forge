package idrac9

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/idrac"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

// VirtualMedia는 iDRAC9의 VirtualMedia 리소스에서 사용하는 필드입니다.
// 필드 구조는 iDRAC10과 같지만, 펌웨어에 따른 리소스 경로 차이를 이
// 패키지에서 관리하기 위해 별도 타입으로 정의합니다.
type VirtualMedia struct {
	ID             string   `json:"Id"`
	MediaTypes     []string `json:"MediaTypes"`
	Inserted       bool     `json:"Inserted"`
	Image          string   `json:"Image"`
	ImageName      string   `json:"ImageName"`
	ConnectedVia   string   `json:"ConnectedVia"`
	WriteProtected bool     `json:"WriteProtected"`
	Actions        struct {
		Insert struct {
			Target string `json:"target"`
		} `json:"#VirtualMedia.InsertMedia"`
		Eject struct {
			Target string `json:"target"`
		} `json:"#VirtualMedia.EjectMedia"`
	} `json:"Actions"`

	uri string // 이 리소스의 @odata.id
}

// URI는 리소스 경로입니다.
func (v *VirtualMedia) URI() string { return v.uri }

// SupportsOpticalInsert는 CD/DVD를 넣을 수 있는 장치인지 확인합니다.
func (v *VirtualMedia) SupportsOpticalInsert() bool {
	if v.Actions.Insert.Target == "" {
		return false
	}
	for _, t := range v.MediaTypes {
		if t == "CD" || t == "DVD" {
			return true
		}
	}
	return false
}

// ListVirtualMedia는 Virtual Media 장치를 URI 순서로 정렬해 반환합니다.
//
// iDRAC9는 펌웨어 세대에 따라 컬렉션 위치가 다릅니다. Dell 참조 스크립트
// (iDRAC-Redfish-Scripting)는 펌웨어 6.00을 경계로 그 아래는
// Managers/iDRAC.Embedded.1/VirtualMedia/{CD,RemovableDisk}, 그 이상은
// Systems/System.Embedded.1/VirtualMedia/{1,2}를 사용합니다. 펌웨어 버전을
// 해석하는 대신 다음 순서로 컬렉션을 찾습니다: System 리소스가 광고한
// 참조 → Systems 아래 표준 경로 → Managers 아래 구형 경로. 앞 후보가
// HTTP 404일 때만 다음으로 넘어가고, 그 밖의 오류(인증·서버 오류 등)는
// 실제 문제이므로 그대로 중단합니다.
func ListVirtualMedia(ctx context.Context, c *redfish.Client) ([]*VirtualMedia, error) {
	candidates := []string{}
	if sys, err := idrac.GetSystem(ctx, c); err == nil && sys.VirtualMedia.ID != "" {
		candidates = append(candidates, sys.VirtualMedia.ID)
	} else if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		logx.Debug("System 조회에 실패해 표준 경로부터 시도합니다: %v", err)
	}
	for _, p := range []string{idrac.SystemPath + "/VirtualMedia", ManagerVirtualMediaPath} {
		if !slices.Contains(candidates, p) {
			candidates = append(candidates, p)
		}
	}

	var collection redfish.Collection
	found := ""
	for i, path := range candidates {
		err := c.Get(ctx, path, &collection)
		if err == nil {
			found = path
			break
		}
		var apiErr *redfish.APIError
		if i == len(candidates)-1 || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			return nil, fmt.Errorf("Virtual Media 장치 목록을 조회하지 못했습니다: %w", err)
		}
		logx.Debug("Virtual Media 컬렉션이 없어(HTTP 404) 다음 후보로 넘어갑니다: %s", path)
	}
	logx.Debug("Virtual Media 컬렉션: %s", found)

	uris := collection.IDs()
	if len(uris) == 0 {
		return nil, fmt.Errorf("Virtual Media 장치를 찾지 못했습니다: %s", found)
	}
	sort.Strings(uris)

	out := make([]*VirtualMedia, 0, len(uris))
	for _, uri := range uris {
		var media VirtualMedia
		if err := c.Get(ctx, uri, &media); err != nil {
			return nil, fmt.Errorf("Virtual Media 장치를 조회하지 못했습니다: %s: %w", uri, err)
		}
		media.uri = uri
		out = append(out, &media)
	}
	return out, nil
}

// scpBootOnceBuffer는 다음 1회 부팅을 Virtual CD/DVD로 설정하는 SCP
// 문서입니다. iDRAC9에서 현장 검증된 셸 스크립트의 값 그대로입니다.
// Dell 참조 스크립트(SetNextOneTimeBootVirtualMediaDeviceOemREDFISH)는
// 같은 속성을 vCD-DVD 표기와 ShareParameters.Target ["IDRAC"]로 보내며,
// 두 형식 모두 같은 iDRAC 속성을 설정합니다. 여기서는 현장 검증 형식을
// 유지합니다.
const scpBootOnceBuffer = `<SystemConfiguration><Component FQDD="iDRAC.Embedded.1">` +
	`<Attribute Name="ServerBoot.1#BootOnce">Enabled</Attribute>` +
	`<Attribute Name="ServerBoot.1#FirstBootDevice">VCD-DVD</Attribute>` +
	`</Component></SystemConfiguration>`

// bootOncePollInterval은 SCP Task와 부트원스 설정 확인의 폴링 간격입니다.
// 테스트에서 짧게 바꿉니다.
var bootOncePollInterval = 2 * time.Second

// scpTaskWait는 SCP Import Task가 끝나기를 기다리는 상한입니다. iDRAC
// 속성만 바꾸는 import도 Lifecycle Controller 작업으로 처리되어 수십 초가
// 걸릴 수 있으므로 redfish.attributeWaitSeconds(기본 5초)와는 별도로
// 둡니다. 테스트에서 짧게 바꿉니다.
var scpTaskWait = 5 * time.Minute

// scpTaskNotFoundGrace는 SCP Import 접수 직후 Task 조회가 404일 때 "아직
// 등록되지 않음"으로 보고 다시 조회하는 유예 시간입니다. Dell 참조
// 스크립트(SetNextOneTimeBootVirtualMediaDeviceOemREDFISH)도 접수 후 잠시
// 기다린 뒤 작업을 조회합니다. 이 시간이 지나도 404면 Task 리소스를
// 노출하지 않는 펌웨어로 판단합니다. 테스트에서 짧게 바꿉니다.
var scpTaskNotFoundGrace = 15 * time.Second

// task는 TaskService 작업 리소스에서 사용하는 필드입니다.
type task struct {
	TaskState  string `json:"TaskState"`
	TaskStatus string `json:"TaskStatus"`
	// PercentComplete는 Redfish 표준 진행률(0~100)입니다. 필드가 없거나
	// JSON null이면 포인터가 nil이라 "정보 없음"입니다. 숫자와 숫자 문자열은
	// 정상 처리하고, 해석할 수 없거나 0~100 범위를 벗어난 값은 완료로
	// 인정하지 않습니다(진행률 안전장치 우회 방지).
	PercentComplete *percent `json:"PercentComplete"`
	Messages        []struct {
		Message   string `json:"Message"`
		MessageID string `json:"MessageId"`
	} `json:"Messages"`
	Oem struct {
		Dell struct {
			JobState string `json:"JobState"`
			Message  string `json:"Message"`
		} `json:"Dell"`
	} `json:"Oem"`
}

// percent는 Redfish 진행률(0~100)입니다. JSON 숫자와 숫자 문자열("100")을
// 모두 받습니다. 필드가 아예 없거나 JSON null이면 이 타입은 만들어지지
// 않으므로(포인터가 nil) 호출자가 "정보 없음"으로 다룹니다. UnmarshalJSON에
// 도달하는 값 중 실제 JSON null(따옴표 없는 null)만 pctNull이고, 따옴표로
// 감싼 ""·"null"처럼 숫자로 해석할 수 없거나 0~100 범위를 벗어난 값은
// pctInvalid입니다. 잘못된 값을 "정보 없음"으로 처리하면 진행률
// 안전장치를 우회할 수 있기 때문입니다.
type percent int

const (
	pctNull    percent = -1 // 실제 JSON null (정보 없음)
	pctInvalid percent = -2 // 필드는 있으나 해석 불가/범위 밖
)

func (p *percent) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		// 포인터 필드의 실제 JSON null은 보통 UnmarshalJSON을 거치지 않지만,
		// 도달하는 경우를 위해 "정보 없음"으로 둡니다. 따옴표로 감싼
		// "null"(문자열)은 아래에서 pctInvalid가 됩니다.
		*p = pctNull
		return nil
	}
	raw := strings.TrimSpace(strings.Trim(s, `"`))
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n < 0 || n > 100 {
		*p = pctInvalid
		return nil
	}
	*p = percent(n)
	return nil
}

// permitsCompletion은 이 진행률이 완료를 허용하는지입니다. 실제 null(정보
// 없음)은 허용하고(다른 신호로 완료 판정), 100은 완료입니다. 100 미만이나
// pctInvalid는 아직 완료로 보지 않습니다(계속 대기).
func (p *percent) permitsCompletion() bool { return *p == pctNull || *p == 100 }

// isNoChange는 SCP Import가 "적용할 변경이 없음"(Dell SYS043)으로 끝난
// 경우인지 판정합니다. 이전 실행이 남긴 BootOnce/FirstBootDevice가 이미
// 원하는 값이면 iDRAC이 이렇게 보고하며, 결과 상태는 원하는 상태와 같으므로
// 성공으로 봅니다(Dell 참조 스크립트도 별도 안내 후 정상 종료).
func (t *task) isNoChange() bool {
	for _, m := range t.Messages {
		if strings.HasSuffix(strings.ToUpper(m.MessageID), "SYS043") ||
			strings.Contains(m.Message, "No changes were applied") {
			return true
		}
	}
	return strings.Contains(t.Oem.Dell.Message, "No changes were applied")
}

func (t *task) messageText() string {
	parts := []string{}
	for _, m := range t.Messages {
		if m.Message != "" {
			parts = append(parts, m.Message)
		}
	}
	if len(parts) == 0 && t.Oem.Dell.Message != "" {
		parts = append(parts, t.Oem.Dell.Message)
	}
	return strings.Join(parts, "; ")
}

// scpJobFailed는 Dell JobState가 실패를 뜻하는 값인지 판정합니다.
// Dell 참조 스크립트(ImportSystemConfiguration*REDFISH.py)와 같이
// Failed와 CompletedWithErrors를 모두 실패로 봅니다.
func scpJobFailed(jobState string) bool {
	return strings.EqualFold(jobState, "Failed") || strings.EqualFold(jobState, "CompletedWithErrors")
}

// waitSCPTask는 SCP Import가 만든 Task가 끝날 때까지 폴링합니다.
// Dell 참조 스크립트와 같이 TaskState Completed(Dell JobState가 Failed·
// CompletedWithErrors가 아니고 TaskStatus가 Critical이 아닐 때)를
// 성공으로, Exception·Killed·Cancelled·CompletedWithErrors와 Dell JobState
// Failed·CompletedWithErrors, TaskStatus Critical을 실패로 봅니다.
// iDRAC9 6.10의 알려진 결함(TaskState는 Completed인데 PercentComplete가
// 100 미만)에 대비해 PercentComplete가 있으면 100이 될 때까지 Completed를
// 인정하지 않습니다(Dell 우회책). 상한 안에 끝나지 않으면 오류입니다 —
// 작업이 아직 진행 중인 상태에서 전원을 켜면 부트원스가 반영되기 전에
// 호스트가 부팅될 수 있기 때문입니다.
//
// Task 조회 오류는 종류별로 다르게 다룹니다(Dell 문서가 401·404·500·503을
// 구분함). HTTP 404는 Task를 아직 한 번도 조회하지 못한 동안에만 "접수
// 직후라 아직 등록되지 않음"으로 보고 scpTaskNotFoundGrace 동안 다시
// 조회하며, 그 뒤에도 404면 Task 리소스를 노출하지 않는 펌웨어로 보고
// 판정 불가(nil, false)를 반환해 호출자가 속성 확인으로 폴백하게
// 합니다. Task를 한 번이라도 정상 조회한 뒤의 404는 리소스 미지원이
// 아니라 일시 오류로 보고 상한 안에서 재시도하며, 끝내 상태를 확인하지
// 못하면 오류입니다(미지원으로 재분류해 속성 폴백 → fail-open으로 흐르면
// 작업 완료를 확인하지 않은 채 전원을 켤 수 있음). 5xx와 연결 오류는
// 작업 처리 중 iDRAC이 일시적으로 내는 것일 수 있어 상한 안에서
// 재시도하고, 그 밖의 4xx(401·403 등)는 즉시 오류입니다. 재시도 끝에도
// 조회하지 못하면 상한 초과와 같이 오류입니다.
func waitSCPTask(ctx context.Context, c *redfish.Client, location string) (tracked bool, err error) {
	pollCtx, cancel := context.WithTimeout(ctx, scpTaskWait)
	defer cancel()
	deadline, _ := pollCtx.Deadline()
	logx.Info("SCP Import 작업의 완료를 기다립니다: %s (최대 %s)", location, scpTaskWait)

	state := ""
	var lastErr error
	var firstNotFound time.Time
	taskSeen := false // Task를 한 번이라도 정상 조회했는지
	for {
		var t task
		if err := c.Get(pollCtx, location, &t); err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			if pollCtx.Err() != nil {
				break
			}
			var apiErr *redfish.APIError
			switch {
			case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && taskSeen:
				// 이미 조회된 적 있는 Task의 404는 미지원이 아닙니다.
				lastErr = err
				logx.Warn("조회되던 SCP Import 작업이 응답하지 않습니다(HTTP 404). 다시 시도합니다: %s", location)
			case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
				if firstNotFound.IsZero() {
					firstNotFound = time.Now()
				}
				if time.Since(firstNotFound) >= scpTaskNotFoundGrace {
					logx.Warn("SCP Import 작업 리소스를 %s 동안 찾을 수 없습니다(HTTP 404): %s",
						scpTaskNotFoundGrace, location)
					return false, nil
				}
				lastErr = err
				logx.Debug("SCP Import 작업이 아직 조회되지 않습니다(HTTP 404). 다시 시도합니다: %s", location)
			case errors.As(err, &apiErr) && apiErr.StatusCode < 500:
				return true, fmt.Errorf("SCP Import 작업을 조회하지 못했습니다: %w", err)
			default:
				lastErr = err
				logx.Warn("SCP Import 작업 조회에 실패해 다시 시도합니다: %v", err)
			}
		} else {
			lastErr = nil
			taskSeen = true
			state = t.TaskState
			switch {
			case strings.EqualFold(state, "Exception"), strings.EqualFold(state, "Killed"),
				strings.EqualFold(state, "Cancelled"), strings.EqualFold(state, "CompletedWithErrors"),
				scpJobFailed(t.Oem.Dell.JobState):
				return true, fmt.Errorf("원타임 Virtual CD/DVD 부트 설정(SCP Import)이 실패했습니다: "+
					"TaskState=%s, TaskStatus=%s, JobState=%s: %s", state, orDash(t.TaskStatus),
					orDash(t.Oem.Dell.JobState), orDash(t.messageText()))
			case strings.EqualFold(state, "Completed"):
				// 진행률 판정을 다른 완료 신호(SYS043·Critical)보다 먼저
				// 둡니다. iDRAC9 6.10의 알려진 결함은 TaskState가 Completed로
				// 뜨는데 진행률이 아직 100 미만인 상황이고(Dell 우회책은
				// 100까지 대기), 이때 SYS043이 함께 오더라도 아직 작업이
				// 진행 중이므로 전원을 켜면 안 됩니다.
				if t.PercentComplete != nil && !t.PercentComplete.permitsCompletion() {
					if *t.PercentComplete == pctInvalid {
						logx.Warn("SCP Import 작업의 진행률(PercentComplete) 값을 해석할 수 없습니다. " +
							"완료로 보지 않고 기다립니다.")
					} else {
						logx.Debug("SCP Import 작업이 Completed로 보고되었지만 진행률이 %d%%입니다. "+
							"100%%까지 기다립니다.", int(*t.PercentComplete))
					}
					break // 계속 폴링합니다(아래 sleep으로).
				}
				// 진행률이 100이거나 정보가 없으면 다른 신호로 판정합니다.
				switch {
				case t.isNoChange():
					logx.Info("SCP Import 완료: 적용할 변경이 없습니다(원타임 부트 설정이 이미 "+
						"되어 있음): %s", orDash(t.messageText()))
					return true, nil
				case strings.EqualFold(t.TaskStatus, "Critical"):
					// Redfish의 TaskStatus는 Health 값(OK/Warning/Critical)이며
					// Critical은 심각한 오류입니다. Completed라도 실패로 봅니다.
					return true, fmt.Errorf("원타임 Virtual CD/DVD 부트 설정(SCP Import)이 "+
						"실패했습니다: TaskState=%s, TaskStatus=%s, JobState=%s: %s", state,
						t.TaskStatus, orDash(t.Oem.Dell.JobState), orDash(t.messageText()))
				case strings.EqualFold(t.TaskStatus, "Warning"):
					logx.Warn("SCP Import가 경고와 함께 완료되었습니다(TaskStatus=Warning): %s",
						orDash(t.messageText()))
					return true, nil
				default:
					if msg := t.messageText(); msg != "" {
						logx.Info("SCP Import 완료: %s", msg)
					} else {
						logx.Info("SCP Import 완료 (TaskStatus=%s)", orDash(t.TaskStatus))
					}
					return true, nil
				}
			default:
				logx.Debug("SCP Import 작업 상태: %s", state)
			}
		}

		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		if err := sleep(pollCtx, min(bootOncePollInterval, remain)); err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			break
		}
	}
	if lastErr != nil {
		return true, fmt.Errorf("SCP Import 작업을 %s 안에 조회하지 못했습니다. "+
			"작업 상태를 알 수 없는 채로 전원을 켜지 않고 중단합니다. "+
			"iDRAC Job Queue에서 %s를 확인한 뒤 다시 실행하세요: %w", scpTaskWait, location, lastErr)
	}
	return true, fmt.Errorf("SCP Import 작업이 %s 안에 끝나지 않았습니다(마지막 상태: %s). "+
		"작업이 진행 중인 상태에서 전원을 켜면 원타임 부트가 반영되지 않을 수 있어 중단합니다. "+
		"iDRAC Job Queue에서 %s를 확인한 뒤 다시 실행하세요", scpTaskWait, orDash(state), location)
}

// setBootOnceVirtualCD는 SCP Import로 다음 1회 부팅을 Virtual CD/DVD로
// 설정하고, 반영을 확인합니다.
//
// SCP Import는 비동기(Job)라 요청 접수(HTTP 202) 후 반영까지 시간이
// 걸립니다. Dell 참조 스크립트와 같이 응답의 Location 헤더가 가리키는
// TaskService 작업이 Completed가 될 때까지 기다린 뒤 진행하고, 작업이
// 실패하거나 상한 안에 끝나지 않으면 전원을 켜지 않고 중단합니다.
// Import 요청은 HostPowerState=Off를 명시합니다. 이 값의 기본은 On이라
// iDRAC이 import를 마치며 호스트를 먼저 켤 수 있기 때문입니다(현장
// 스크립트에는 없던 인자로, 전원 ON 시점을 이 도구가 갖기 위한 것).
//
// Location이 없거나 Task 리소스가 없는(HTTP 404가 유예 시간 뒤에도
// 계속) 펌웨어에서는 결과인 DellAttributes 값을 limit 안에서 폴링해
// 반영을 확인합니다(pollBootOnceAttributes — 404만 fail-open). 요청 접수
// 자체가 거부되면 오류입니다.
func setBootOnceVirtualCD(ctx context.Context, c *redfish.Client, limit time.Duration) error {
	target := scpImportTarget(ctx, c)
	_, location, err := c.PostLocation(ctx, target, map[string]any{
		"ShareParameters": map[string]string{"Target": "ALL"},
		"ImportBuffer":    scpBootOnceBuffer,
		"HostPowerState":  "Off",
	})
	if err != nil {
		return fmt.Errorf("원타임 Virtual CD/DVD 부트 설정(SCP Import)에 실패했습니다: %w", err)
	}
	logx.Info("원타임 부트 설정(SCP Import)을 요청했습니다.")

	if location != "" {
		tracked, err := waitSCPTask(ctx, c, location)
		if err != nil {
			return err
		}
		if tracked {
			// 작업 완료 후 결과를 한 번 확인합니다. 확인 경로가 없는 펌웨어를
			// 위해 실패는 경고에 그칩니다.
			verifyBootOnceOnce(ctx, c)
			return nil
		}
	} else {
		logx.Warn("SCP Import 응답에 작업 위치(Location)가 없어 결과 속성으로 반영을 확인합니다.")
	}

	return pollBootOnceAttributes(ctx, c, limit)
}

// pollBootOnceAttributes는 DellAttributes를 limit 안에서 폴링해 원타임
// 부트 설정의 반영을 확인합니다(Task를 추적할 수 없을 때의 경로).
//
// 조회 오류는 waitSCPTask와 같은 기준으로 나눕니다: HTTP 404(구형 펌웨어는
// DellAttributes 리소스 자체가 없음)만 확인 불가로 보고 경고 후
// 진행합니다(fail-open, 확정된 운영 정책). 5xx·연결 오류는 limit 안에서
// 재시도하고 끝내 조회하지 못하면 오류, 그 밖의 4xx(401·403 등)는 즉시
// 오류입니다 — 확인 리소스가 있는데 읽지 못하는 것은 "확인 불가"가
// 아니라 실제 문제이기 때문입니다. 조회 자체가 limit에 걸려 끝나지
// 않은 경우(느린 GET)도 값을 읽지 못한 것이므로 오류입니다. limit 안에
// 값을 읽었으나 기대와 같아지지 않은 경우에만 경고 후 진행합니다
// (fail-open, 확정된 운영 정책).
func pollBootOnceAttributes(ctx context.Context, c *redfish.Client, limit time.Duration) error {
	logx.Info("부트 설정 반영을 확인합니다. (최대 %s)", limit)
	pollCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	deadline, _ := pollCtx.Deadline()

	var first, bootOnce string
	var lastErr error
	for {
		attrs, err := idrac.GetAttributes(pollCtx, c)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if pollCtx.Err() != nil {
				// 조회가 제한 시간에 걸려 끝났습니다. 마지막 값을 읽지
				// 못했으므로 fail-open이 아니라 오류로 처리합니다.
				lastErr = err
				break
			}
			var apiErr *redfish.APIError
			switch {
			case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
				logx.Warn("이 펌웨어에는 부트 설정 확인 경로(DellAttributes)가 없습니다(HTTP 404). " +
					"계속 진행합니다.")
				logx.Warn("실제 ISO 부팅 여부는 iDRAC Virtual Console에서 확인하세요.")
				return nil
			case errors.As(err, &apiErr) && apiErr.StatusCode < 500:
				return fmt.Errorf("부트 설정을 확인하지 못했습니다: %w", err)
			}
			lastErr = err
			logx.Warn("부트 설정 조회에 실패해 다시 시도합니다: %v", err)
		} else {
			lastErr = nil
			first, bootOnce = bootOnceValues(attrs)
			if bootOnceApplied(first, bootOnce) {
				logx.Info("부트 설정 확인: FirstBootDevice=%s, BootOnce=%s", first, bootOnce)
				return nil
			}
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		if err := sleep(pollCtx, min(bootOncePollInterval, remain)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
	}
	if lastErr != nil {
		return fmt.Errorf("부트 설정을 %s 안에 확인하지 못했습니다. "+
			"설정 상태를 알 수 없는 채로 전원을 켜지 않고 중단합니다: %w", limit, lastErr)
	}
	logx.Warn("제한 시간 안에 부트 설정 반영이 확인되지 않았습니다"+
		"(현재: FirstBootDevice=%s, BootOnce=%s). 계속 진행합니다.", first, bootOnce)
	logx.Warn("실제 ISO 부팅 여부는 iDRAC Virtual Console에서 확인하세요.")
	return nil
}

// bootOnceValues는 DellAttributes에서 원타임 부트 관련 값을 꺼냅니다.
func bootOnceValues(attrs *idrac.Attributes) (first, bootOnce string) {
	return attrs.String("ServerBoot.1.FirstBootDevice"), attrs.String("ServerBoot.1.BootOnce")
}

// bootOnceApplied는 원타임 Virtual CD/DVD 부트가 설정된 값인지 판정합니다.
// 값 표기는 펌웨어에 따라 VCD-DVD(iDRAC10, 현장 스크립트)와 vCD-DVD(Dell
// 참조 스크립트)가 모두 보이므로 대소문자를 구분하지 않고 비교합니다.
func bootOnceApplied(first, bootOnce string) bool {
	return strings.EqualFold(first, "VCD-DVD") && strings.EqualFold(bootOnce, "Enabled")
}

// verifyBootOnceOnce는 Task 완료 후 결과 속성을 한 번 확인합니다.
// 판정은 이미 Task 완료로 끝났으므로(이 확인은 실기 관찰용 보조 정보)
// 조회 실패와 불일치는 경고만 남깁니다. 404는 확인 경로가 없는 펌웨어로
// 안내하고, 그 밖의 오류는 오류 내용을 그대로 남깁니다.
func verifyBootOnceOnce(ctx context.Context, c *redfish.Client) {
	attrs, err := idrac.GetAttributes(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		var apiErr *redfish.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			logx.Warn("이 펌웨어에는 부트 설정 확인 경로(DellAttributes)가 없습니다(HTTP 404). " +
				"SCP Import 작업은 완료되었으므로 계속 진행합니다.")
			return
		}
		logx.Warn("부트 설정 결과를 확인하지 못했습니다. SCP Import 작업은 완료되었으므로 "+
			"계속 진행합니다: %v", err)
		return
	}
	first, bootOnce := bootOnceValues(attrs)
	if bootOnceApplied(first, bootOnce) {
		logx.Info("부트 설정 확인: FirstBootDevice=%s, BootOnce=%s", first, bootOnce)
		return
	}
	logx.Warn("SCP Import는 완료되었지만 속성 값이 기대와 다릅니다"+
		"(FirstBootDevice=%s, BootOnce=%s). 계속 진행합니다.", first, bootOnce)
	logx.Warn("실제 ISO 부팅 여부는 iDRAC Virtual Console에서 확인하세요.")
}

// Boot는 지정한 ISO로 서버를 원타임 가상 미디어 부팅합니다.
// 단계 구성은 iDRAC10 드라이버와 같고, iDRAC9 차이(가상 미디어 위치와
// 액션 경로의 광고 값 사용, Image만 보내는 InsertMedia, SCP Task 완료를
// 기다리는 부트원스 설정, 안정성 속성 단계 없음, Settings 부재 시 부트
// 순서 보호 생략)만 다릅니다.
func Boot(ctx context.Context, c *redfish.Client, req bmc.BootRequest) error {
	if req.ISOURL == "" {
		return fmt.Errorf("마운트할 ISO URL이 비어 있습니다")
	}
	if req.HTTPTimeout <= 0 {
		req.HTTPTimeout = 60 * time.Second
	}

	// 1. ISO 파일 접근 확인
	if !req.SkipISOCheck {
		if err := idrac.CheckISOURL(ctx, req.ISOURL, req.HTTPTimeout); err != nil {
			return err
		}
		logx.Info("ISO 파일 접근 성공")
	}

	// 2~3. Virtual Media 장치 목록 조회와 선택
	devices, err := ListVirtualMedia(ctx, c)
	if err != nil {
		return err
	}
	var selected *VirtualMedia
	for _, device := range devices {
		if device.SupportsOpticalInsert() {
			logx.Debug("장치 %s: %v 사용 가능", device.ID, device.MediaTypes)
			if selected == nil {
				selected = device
			}
			continue
		}
		logx.Debug("장치 %s: %v 사용 불가 (건너뜀)", device.ID, device.MediaTypes)
	}
	if selected == nil {
		return fmt.Errorf("ISO를 넣을 수 있는 Virtual Media 장치가 없습니다")
	}
	logx.Info("선택된 Virtual Media 장치: %s", selected.ID)

	// 4. 서버 전원 OFF — 폴링과 fail-open 정책은 iDRAC10과 같습니다.
	// 이미 꺼져 있으면(이전 실행이 SCP 단계에서 중단된 뒤 재실행 등)
	// ForceOff를 보내지 않습니다. iDRAC9는 꺼진 서버에 대한 전원 끄기
	// 요청을 거부할 수 있어, 그대로 보내면 재실행이 막힙니다.
	if state := powerState(ctx, c); state == "Off" {
		logx.Info("서버가 이미 꺼져 있습니다. 전원 끄기를 건너뜁니다.")
	} else {
		logx.Info("서버 전원을 끄고 꺼짐을 확인합니다. (최대 %s)", req.Waits.PowerOff)
		if err := idrac.Reset(ctx, c, "ForceOff"); err != nil {
			return err
		}
		if err := waitPowerOff(ctx, c, req.Waits.PowerOff); err != nil {
			return err
		}
	}

	// 5. 기존 Virtual Media 모두 제거
	// 빈 장치에 EjectMedia를 호출하면 오류가 나므로 Inserted를 먼저 확인합니다.
	if err := ejectAll(ctx, c, devices, req.Waits.Media); err != nil {
		return err
	}

	// iDRAC10 드라이버의 5-1 단계(VirtualMedia 안정성 속성 —
	// Attached/EncryptEnable 변경)는 iDRAC9에서 수행하지 않습니다.
	// 검증된 셸 스크립트에 없던 BMC 설정 변경이고, 특히 EncryptEnable을
	// 끄면 가상 미디어 트래픽 암호화가 해제되는 부작용이 있어 실기
	// 검증 없이 기본 동작으로 넣지 않습니다. 필요하면 명시적 opt-in
	// 설정으로 추가합니다.

	// 5-2. 부트 순서에서 Virtual CD/DVD 제거 (전 단계 fail-open 보호 기능)
	if err := removeVirtualMediaFromBootOrder(ctx, c); err != nil {
		return err
	}

	// 6. ISO 마운트
	// Redfish 규약대로 POST 대상은 장치가 광고한 Actions.target입니다
	// (컬렉션 위치처럼 액션 경로도 펌웨어에 따라 다를 수 있음). 본문은
	// 현장 검증된 셸 스크립트와 같이 Image만 보냅니다. Dell 참조
	// 스크립트는 Inserted/WriteProtected도 함께 보내지만(모든 펌웨어
	// 세대에서 동일) 두 값은 iDRAC의 기본값과 같아 결과가 같습니다.
	logx.Info("ISO를 마운트합니다.")
	if _, err := c.Post(ctx, selected.Actions.Insert.Target, map[string]any{
		"Image": req.ISOURL,
	}); err != nil {
		return fmt.Errorf("ISO 마운트에 실패했습니다: %w", err)
	}
	if err := sleep(ctx, req.Waits.Media); err != nil {
		return err
	}

	// 7. ISO 마운트 결과 확인
	// ConnectedVia 표기는 iDRAC9 펌웨어에 따라 다를 수 있어 기록만 하고,
	// 성공 조건은 Inserted와 Image 일치로 판정합니다.
	var mounted VirtualMedia
	if err := c.Get(ctx, selected.URI(), &mounted); err != nil {
		return fmt.Errorf("ISO 마운트 결과를 확인하지 못했습니다: %w", err)
	}
	logx.Info("마운트 상태: Inserted=%t, ConnectedVia=%s, ImageName=%s",
		mounted.Inserted, orDash(mounted.ConnectedVia), orDash(mounted.ImageName))
	if !mounted.Inserted || mounted.Image != req.ISOURL {
		return fmt.Errorf("ISO 마운트 확인에 실패했습니다: Inserted=%t, Image=%s",
			mounted.Inserted, mounted.Image)
	}
	logx.Info("ISO 마운트 확인 성공")

	// 8~9. 다음 1회 부팅을 Virtual CD/DVD로 설정(SCP Import)하고 작업 완료 확인
	if err := setBootOnceVirtualCD(ctx, c, req.Waits.Attribute); err != nil {
		return err
	}

	// 10. 서버 전원 ON
	// SCP Import는 HostPowerState=Off로 요청했으므로 여기서 켜는 것이
	// 첫 전원 ON이어야 합니다. 그 전제가 깨져 이미 켜져 있으면(펌웨어가
	// 인자를 무시하고 import 끝에 켠 경우) 켜기 요청이 거부될 수 있으므로
	// 경고만 남기고 건너뜁니다 — 원타임 부트는 이미 설정된 뒤라 부팅
	// 자체는 ISO로 진행됩니다. 실기 관찰용으로 직전 상태를 debug에 남깁니다.
	state := powerState(ctx, c)
	logx.Debug("전원 ON 직전 상태: PowerState=%s", orDash(state))
	if state == "On" {
		logx.Warn("서버가 이미 켜져 있습니다(SCP Import 과정에서 켜졌거나, 앞서 전원 꺼짐이 확인되지 " +
			"않은 채 계속 켜져 있던 경우). 전원 켜기를 건너뜁니다.")
		logx.Warn("후자라면 다음 재부팅 전까지 ISO 부팅이 시작되지 않으므로 iDRAC Virtual Console에서 확인하세요.")
		logx.Info("%s 기다립니다.", req.Waits.PowerOn)
	} else {
		logx.Info("서버 전원을 켜고 %s 기다립니다.", req.Waits.PowerOn)
		if err := idrac.Reset(ctx, c, "On"); err != nil {
			return err
		}
	}
	if err := sleep(ctx, req.Waits.PowerOn); err != nil {
		return err
	}

	// 11. 최종 상태 확인
	sys, err := idrac.GetSystem(ctx, c)
	if err != nil {
		return fmt.Errorf("최종 상태를 확인하지 못했습니다: %w", err)
	}
	logx.Info("최종 상태: PowerState=%s, Health=%s", sys.PowerState, sys.Status.Health)
	logx.Info("실제 ISO 부팅 여부는 iDRAC Virtual Console에서 최종 확인하세요.")
	logx.Info("설치가 끝나면 `upi-forge eject`로 미디어를 제거하세요.")
	return nil
}

// powerState는 현재 PowerState를 반환합니다. 조회에 실패하면 빈 문자열이며,
// 호출자는 상태를 모르는 것으로 보고 기존 절차(전원 요청 전송)를 따릅니다.
func powerState(ctx context.Context, c *redfish.Client) string {
	sys, err := idrac.GetSystem(ctx, c)
	if err != nil {
		if ctx.Err() == nil {
			logx.Debug("전원 상태를 조회하지 못했습니다: %v", err)
		}
		return ""
	}
	return sys.PowerState
}

// ejectAll은 삽입된 미디어를 모두 제거합니다.
// 장치별 상태를 먼저 확인하는 절차는 iDRAC10과 같습니다.
func ejectAll(ctx context.Context, c *redfish.Client, devices []*VirtualMedia, wait time.Duration) error {
	for _, device := range devices {
		var current VirtualMedia
		if err := c.Get(ctx, device.URI(), &current); err != nil {
			return fmt.Errorf("Virtual Media 상태를 확인하지 못했습니다: %s: %w", device.URI(), err)
		}
		if !current.Inserted {
			logx.Debug("장치 %s: 연결된 미디어가 없습니다.", device.ID)
			continue
		}
		logx.Info("장치 %s의 미디어를 제거합니다. (ImageName=%s)", device.ID, current.ImageName)
		if _, err := c.Post(ctx, ejectTarget(device, &current), map[string]any{}); err != nil {
			return fmt.Errorf("Virtual Media 제거에 실패했습니다: %s: %w", device.ID, err)
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
	return nil
}

// Eject는 삽입된 모든 Virtual Media를 제거하고 결과를 검증합니다.
// (장치 목록 위치만 다르고 절차는 idrac.Eject와 같습니다.)
func Eject(ctx context.Context, c *redfish.Client, wait time.Duration) error {
	devices, err := ListVirtualMedia(ctx, c)
	if err != nil {
		return err
	}
	ejected := 0
	for _, device := range devices {
		var current VirtualMedia
		if err := c.Get(ctx, device.URI(), &current); err != nil {
			return fmt.Errorf("Virtual Media 상태를 확인하지 못했습니다: %s: %w", device.URI(), err)
		}
		logx.Info("장치 %s: Inserted=%t, ImageName=%s", device.ID, current.Inserted, orDash(current.ImageName))
		if !current.Inserted {
			continue
		}
		if _, err := c.Post(ctx, ejectTarget(device, &current), map[string]any{}); err != nil {
			return fmt.Errorf("Virtual Media 제거에 실패했습니다: %s: %w", device.ID, err)
		}
		ejected++
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}

	remaining := 0
	for _, device := range devices {
		var current VirtualMedia
		if err := c.Get(ctx, device.URI(), &current); err != nil {
			return fmt.Errorf("Virtual Media 상태를 확인하지 못했습니다: %s: %w", device.URI(), err)
		}
		logx.Info("장치 %s: Inserted=%t, ConnectedVia=%s, ImageName=%s",
			device.ID, current.Inserted, orDash(current.ConnectedVia), orDash(current.ImageName))
		if current.Inserted {
			remaining++
		}
	}
	if remaining != 0 {
		return fmt.Errorf("제거되지 않은 Virtual Media가 %d개 있습니다", remaining)
	}
	logx.Info("Virtual Media 제거 완료: %d개. 모든 장치가 Inserted=false 상태입니다.", ejected)
	return nil
}

// ejectTarget은 EjectMedia의 POST 대상입니다. Redfish 규약대로 장치가
// 광고한 Actions.target을 사용하고(다시 읽은 현재 상태 우선), 광고가
// 없는 펌웨어에서만 리소스 경로에서 조합한 관례 경로로 폴백합니다.
func ejectTarget(device, current *VirtualMedia) string {
	if t := current.Actions.Eject.Target; t != "" {
		return t
	}
	if t := device.Actions.Eject.Target; t != "" {
		return t
	}
	return device.URI() + "/Actions/VirtualMedia.EjectMedia"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var virtualOpticalName = regexp.MustCompile(`(?i)virtual (optical|cd|dvd)`)

// isVirtualMediaBootOption은 부트 항목이 가상 미디어인지 판단합니다.
// RelatedItem과 표시 이름을 확인하는 기준은 iDRAC10과 같습니다.
func isVirtualMediaBootOption(o idrac.BootOption) bool {
	for _, item := range o.RelatedItem {
		if strings.Contains(item.ID, "VirtualMedia") {
			return true
		}
	}
	return virtualOpticalName.MatchString(o.DisplayName)
}

// removeVirtualMediaFromBootOrder는 평상시 부트 순서에서 가상 CD/DVD를
// 제외합니다. 목적과 전 단계 fail-open 정책, 리소스 경로(Systems·Settings)는
// iDRAC10과 같습니다. Settings 리소스가 없는 펌웨어(HTTP 404)에서는
// 보호 단계를 경고 후 건너뜁니다 — ComputerSystem에 직접 PATCH하는
// 방식은 iDRAC9 문서에 근거가 없고(Dell이 문서화한 대체 경로는
// Systems/<ID>/BootSources/Settings의 Attributes이나 그 배열 형식을
// 확인하지 못함), 검증 없는 쓰기를 기본 동작에 넣지 않습니다.
// iDRAC10과 같은 보호 절차를 사용하되, Settings 조회의 404 처리는 위
// 정책에 따라 다르게 처리합니다.
func removeVirtualMediaFromBootOrder(ctx context.Context, c *redfish.Client) error {
	sys, err := idrac.GetSystem(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logx.Warn("부트 순서를 조회하지 못해 가상 미디어 제외를 건너뜁니다. "+
			"설치 후 부트 순서를 직접 확인하세요: %v", err)
		return nil
	}
	order := sys.Boot.BootOrder
	if len(order) == 0 {
		logx.Debug("부트 순서 정보가 없어 변경하지 않습니다.")
		return nil
	}

	var keep, drop []string
	for _, ref := range order {
		var option idrac.BootOption
		if err := c.Get(ctx, idrac.SystemPath+"/BootOptions/"+ref, &option); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logx.Warn("부트 항목을 조회하지 못해 그대로 유지합니다: %s: %v", ref, err)
			keep = append(keep, ref)
			continue
		}
		if isVirtualMediaBootOption(option) {
			logx.Info("  %s : %s  <- 가상 미디어 (부트 순서에서 제외)", ref, option.DisplayName)
			drop = append(drop, ref)
			continue
		}
		logx.Debug("  %s : %s", ref, option.DisplayName)
		keep = append(keep, ref)
	}

	switch {
	case len(drop) == 0:
		logx.Info("부트 순서에 가상 미디어 항목이 없습니다. 변경하지 않습니다.")
		return nil
	case len(keep) == 0:
		logx.Warn("가상 미디어를 빼면 부트 순서가 비게 됩니다. 변경하지 않습니다.")
		return nil
	}

	payload := map[string]any{"Boot": map[string]any{"BootOrder": keep}}
	if _, err := c.Patch(ctx, idrac.SystemSettingsPath, payload); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var apiErr *redfish.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			logx.Warn("이 펌웨어에는 부트 순서 Settings 리소스가 없어(HTTP 404) 가상 미디어 제외를 건너뜁니다. " +
				"설치 후 BIOS 부트 순서에 가상 CD/DVD가 남아 있는지 직접 확인하세요.")
			return nil
		}
		logx.Warn("부트 순서 변경 요청이 거부되었습니다. 원타임 부트 설정으로 계속 진행합니다: %v", err)
		return nil
	}
	logx.Info("부트 순서 변경이 등록되었습니다. 다음 부팅에서 적용됩니다.")
	return nil
}
