package idrac9

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/idrac"
	"upi-forge/internal/redfish"
)

// fakeIDRAC9는 iDRAC9 Redfish의 최소 동작을 재현하는 테스트 서버입니다.
// iDRAC10 테스트(internal/idrac/boot_test.go)와 같은 방식이며, iDRAC9의
// 차이(Managers 아래 Virtual Media, SCP Import, 비동기 반영, 구형 펌웨어의
// DellAttributes 부재)를 시나리오로 재현합니다.
type fakeIDRAC9 struct {
	t          *testing.T
	mu         sync.Mutex
	attributes map[string]string
	media      map[string]*fakeMedia
	bootOrder  []string
	settings   []string
	resets     []string
	powerState string

	// systemsCollection이 true면 Systems 아래 Virtual Media 컬렉션이
	// 존재합니다(펌웨어 6.00 이상). false면 그 경로는 404이고 Managers
	// 아래 컬렉션만 있습니다(구형 펌웨어).
	systemsCollection bool
	// systemAdvertisesVM이 true면 System 리소스가 Virtual Media 컬렉션
	// 참조를 광고합니다(systemsCollection과 함께 씀). false면 광고 없이
	// 컬렉션만 존재하는 펌웨어를 재현합니다.
	systemAdvertisesVM bool
	// failSystemsCollection이 true면 Systems 아래 컬렉션 조회에 500을
	// 반환합니다. 404가 아닌 오류에는 폴백하지 않아야 합니다.
	failSystemsCollection bool
	// noSettingsResource가 true면 Systems/.../Settings가 없는 펌웨어를
	// 재현합니다(PATCH 404). 드라이버는 보호 단계를 건너뛰어야 합니다.
	noSettingsResource bool
	// scpDeviceValue는 SCP 반영 후 iDRAC이 보고하는 FirstBootDevice 표기입니다.
	// 펌웨어에 따라 VCD-DVD 또는 vCD-DVD로 보고됩니다.
	scpDeviceValue string
	systemPatches  [][]string // ComputerSystem에 직접 쓴 BootOrder(있으면 안 됨)
	// noDellAttributes가 true면 DellAttributes 리소스가 없는 구형
	// 펌웨어를 재현합니다(GET/PATCH 모두 404).
	noDellAttributes bool
	// scpApplyAfterPolls는 Location 없는 펌웨어(noTaskLocation)에서
	// SCP Import 접수 후 몇 번째 속성 조회부터 반영된 값을 보고할지입니다.
	// 0이면 즉시 반영.
	scpApplyAfterPolls int
	// failSCPImport가 true면 SCP Import 요청을 거부합니다.
	failSCPImport bool
	// noTaskLocation이 true면 SCP Import 응답에 Location 헤더를 넣지 않아
	// Task를 추적할 수 없는 펌웨어를 재현합니다.
	noTaskLocation bool
	// taskPollsUntilDone은 Task 조회 몇 번째부터 최종 상태를 보고할지입니다.
	// 그 전에는 Running입니다. 속성 반영도 이 시점에 이루어집니다.
	taskPollsUntilDone int
	// taskFinalState는 Task의 최종 상태입니다. 비우면 Completed입니다.
	taskFinalState string
	// taskNeverCompletes가 true면 Task가 계속 Running으로 남습니다.
	taskNeverCompletes bool
	// taskDellJobState는 Task의 Oem.Dell.JobState입니다. Dell은 TaskState가
	// Completed여도 JobState Failed로 실패를 알리는 경우가 있습니다.
	taskDellJobState string
	// taskUnavailable이 true면 Location은 주지만 그 Task 조회는 404입니다.
	// Task 리소스를 노출하지 않는 펌웨어를 재현합니다.
	taskUnavailable bool
	// taskErrorStatus가 0이 아니면 Task 조회에 그 HTTP 상태를 반환합니다.
	// taskErrorsUntil이 0보다 크면 처음 그 횟수만 오류이고 이후는 정상입니다.
	// 일시적 5xx를 재현하며, 0이면 계속 오류입니다.
	taskErrorStatus int
	taskErrorsUntil int
	// taskNotFoundUntil이 0보다 크면 처음 그 횟수의 Task 조회가 404이고
	// 이후는 정상입니다. 접수 직후 아직 등록되지 않은 작업을 재현합니다.
	taskNotFoundUntil int
	// taskVanishAfter가 0보다 크면 그 횟수의 정상 조회 뒤부터 Task 조회가
	// 404입니다(조회되던 작업이 사라지는 상황을 재현합니다). taskVanishFor가 0보다 크면
	// 그 횟수만 404이고 이후 다시 정상, 0이면 계속 404입니다.
	taskVanishAfter int
	taskVanishFor   int
	// attrErrorStatus가 0이 아니면 DellAttributes GET에 그 HTTP 상태를
	// 반환합니다. attrErrorsUntil이 0보다 크면 처음 그 횟수만 오류입니다.
	attrErrorStatus int
	attrErrorsUntil int
	// attrDelay가 0보다 크면 DellAttributes GET 응답을 그만큼 늦춰
	// 응답이 느린 iDRAC을 재현합니다.
	attrDelay time.Duration
	// taskStatus는 Task의 TaskStatus(Health)입니다. 비우면 OK입니다.
	taskStatus string
	// taskPercentLagPolls가 0보다 크면 최종 상태 보고 뒤 처음 그 횟수
	// 동안 PercentComplete를 43으로 보고합니다(iDRAC9 6.10의 알려진
	// 결함: TaskState는 Completed인데 진행률이 100 미만). 음수면 영원히
	// 43입니다. 0이면 100입니다.
	taskPercentLagPolls int
	// taskPercentAsString이 true면 PercentComplete를 문자열("100")로
	// 보고합니다(표기 차이를 재현합니다).
	taskPercentAsString bool
	// taskPercentInvalid가 true면 PercentComplete를 해석 불가한 문자열
	// "43%"로 보고합니다(비숫자 표기를 재현합니다). 여러 잘못된 값의 파싱은
	// TestPercentParsing이 단위로 검증합니다.
	taskPercentInvalid bool
	// taskMessageID가 비어 있지 않으면 최종 상태의 Messages[0].MessageId로
	// 보고합니다(예: "IDRAC.2.8.SYS043" — 적용할 변경 없음).
	taskMessageID string
	// scpPowersOn이 true면 SCP Task가 완료될 때 iDRAC이 호스트를 켭니다.
	// HostPowerState=Off를 무시하는 펌웨어를 재현합니다.
	scpPowersOn bool

	attributeGets  int
	taskGets       int
	scpBuffers     []string // 접수된 ImportBuffer
	scpPowerStates []string // 접수된 HostPowerState
	insertBodies   []string // InsertMedia의 원본 요청 본문
	requestedPaths []string // 와이어 검증용: 실제 요청 순서(메서드 경로)
}

type fakeMedia struct {
	id           string
	mediaTypes   []string
	insertable   bool
	inserted     bool
	image        string
	connectedVia string
}

func newFakeIDRAC9(t *testing.T) *fakeIDRAC9 {
	return &fakeIDRAC9{
		t:          t,
		attributes: map[string]string{"ServerBoot.1.FirstBootDevice": "Normal"},
		powerState: "On",
		// 구형 iDRAC9의 장치 구성: CD(광학)와 RemovableDisk.
		// 이전 실행이 남긴 ISO가 CD에 꽂혀 있는 상태에서 시작합니다.
		media: map[string]*fakeMedia{
			"CD": {id: "CD", mediaTypes: []string{"CD", "DVD"}, insertable: true,
				inserted: true, image: "http://old/stale.iso", connectedVia: "URI"},
			"RemovableDisk": {id: "RemovableDisk", mediaTypes: []string{"USBStick"}},
		},
		bootOrder:      []string{"Boot0001", "Boot0002"},
		scpDeviceValue: "VCD-DVD",
	}
}

// vmBase는 현재 시나리오의 Virtual Media 컬렉션 기본 경로입니다.
func (f *fakeIDRAC9) vmBase() string {
	if f.systemsCollection {
		return idrac.SystemPath + "/VirtualMedia"
	}
	return ManagerVirtualMediaPath
}

func (f *fakeIDRAC9) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// 느린 응답 재현은 잠금 밖에서 수행합니다(그동안 다른 요청 — 클라이언트의
		// 제한 시간 뒤 정리 요청 등 — 이 처리될 수 있어야 함).
		if f.attrDelay > 0 && r.Method == http.MethodGet && path == idrac.AttributesPath {
			time.Sleep(f.attrDelay)
		}
		// 제한 시간에 걸린 요청의 핸들러가 아직 실행 중일 때 다음 요청이
		// 들어올 수 있으므로 상태 접근을 직렬화합니다.
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requestedPaths = append(f.requestedPaths, r.Method+" "+path)
		writeJSON := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}

		switch {
		case r.Method == http.MethodPost && path == "/redfish/v1/SessionService/Sessions":
			w.Header().Set("X-Auth-Token", "tok")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/1")
			w.WriteHeader(http.StatusCreated)

		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && path == idrac.SystemPath+"/Actions/ComputerSystem.Reset":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.resets = append(f.resets, body["ResetType"])
			// 이미 그 상태인 서버에 대한 전원 요청은 거부합니다(iDRAC9가
			// 꺼진 서버에 ForceOff, 켜진 서버에 On을 거부하는 동작을 재현합니다).
			switch body["ResetType"] {
			case "ForceOff":
				if f.powerState == "Off" {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":{"message":"server is already powered off"}}`))
					return
				}
				f.powerState = "Off"
			case "On":
				if f.powerState == "On" {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":{"message":"server is already powered on"}}`))
					return
				}
				f.powerState = "On"
			}
			writeJSON(map[string]any{})

		case r.Method == http.MethodGet && path == idrac.SystemPath:
			payload := map[string]any{
				"Id": "System.Embedded.1", "Model": "PowerEdge R650",
				"PowerState": f.powerState,
				"Status":     map[string]string{"Health": "OK"},
				"Boot":       map[string]any{"BootOrder": f.bootOrder},
			}
			if f.systemAdvertisesVM {
				payload["VirtualMedia"] = map[string]string{"@odata.id": f.vmBase()}
			}
			writeJSON(payload)

		case r.Method == http.MethodGet && path == ManagerPath:
			writeJSON(map[string]any{
				"Actions": map[string]any{
					"Oem": map[string]any{
						"#OemManager.ImportSystemConfiguration": map[string]string{
							"target": scpImportPath,
						},
					},
				},
			})

		case r.Method == http.MethodPost && path == scpImportPath:
			if f.failSCPImport {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"SYS041"}}`))
				return
			}
			var body struct {
				ImportBuffer   string `json:"ImportBuffer"`
				HostPowerState string `json:"HostPowerState"`
				ShareParams    struct {
					Target string `json:"Target"`
				} `json:"ShareParameters"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.ShareParams.Target != "ALL" {
				f.t.Errorf("ShareParameters.Target은 ALL이어야 합니다: %q", body.ShareParams.Target)
			}
			f.scpBuffers = append(f.scpBuffers, body.ImportBuffer)
			f.scpPowerStates = append(f.scpPowerStates, body.HostPowerState)
			// 비동기 Job 재현: Location의 Task가 taskPollsUntilDone 이후에
			// 끝나고 그때 속성이 반영됩니다. Location이 없는 경우에는
			// 반영 시점을 scpApplyAfterPolls(속성 조회 횟수)가 정합니다.
			if !f.noTaskLocation {
				w.Header().Set("Location", taskPath)
			}
			w.WriteHeader(http.StatusAccepted)

		case r.Method == http.MethodGet && path == taskPath:
			f.taskGets++
			if f.noTaskLocation || f.taskUnavailable {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if f.taskGets <= f.taskNotFoundUntil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if f.taskVanishAfter > 0 && f.taskGets > f.taskVanishAfter &&
				(f.taskVanishFor == 0 || f.taskGets <= f.taskVanishAfter+f.taskVanishFor) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if f.taskErrorStatus != 0 && (f.taskErrorsUntil == 0 || f.taskGets <= f.taskErrorsUntil) {
				w.WriteHeader(f.taskErrorStatus)
				return
			}
			state := "Running"
			if !f.taskNeverCompletes && f.taskGets > f.taskPollsUntilDone {
				state = f.taskFinalState
				if state == "" {
					state = "Completed"
				}
			}
			status := f.taskStatus
			if status == "" {
				status = "OK"
			}
			percent := 100
			if state == "Running" {
				percent = 10
			} else if f.taskPercentLagPolls < 0 || f.taskGets-f.taskPollsUntilDone <= f.taskPercentLagPolls {
				percent = 43
			}
			var percentValue any = percent
			if f.taskPercentAsString {
				percentValue = strconv.Itoa(percent)
			}
			if f.taskPercentInvalid {
				percentValue = "43%"
			}
			message := map[string]string{"Message": "SCP " + state}
			if f.taskMessageID != "" && state != "Running" {
				message["MessageId"] = f.taskMessageID
				message["Message"] = "No changes were applied since the current component configuration matched the requested configuration"
			}
			if f.scpPowersOn && state == "Completed" && percent == 100 {
				f.powerState = "On"
			}
			payload := map[string]any{
				"Id": "JID_1", "TaskState": state, "TaskStatus": status,
				"PercentComplete": percentValue,
				"Messages":        []map[string]string{message},
			}
			if f.taskDellJobState != "" && state != "Running" {
				payload["Oem"] = map[string]any{"Dell": map[string]string{"JobState": f.taskDellJobState}}
			}
			writeJSON(payload)

		case r.Method == http.MethodPatch && path == idrac.SystemSettingsPath:
			if f.noSettingsResource {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var body struct {
				Boot struct {
					BootOrder []string `json:"BootOrder"`
				} `json:"Boot"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.settings = body.Boot.BootOrder
			writeJSON(map[string]any{})

		case r.Method == http.MethodPatch && path == idrac.SystemPath:
			var body struct {
				Boot struct {
					BootOrder []string `json:"BootOrder"`
				} `json:"Boot"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.systemPatches = append(f.systemPatches, body.Boot.BootOrder)
			writeJSON(map[string]any{})

		case r.Method == http.MethodGet && path == idrac.SystemPath+"/VirtualMedia" && f.failSystemsCollection:
			w.WriteHeader(http.StatusInternalServerError)

		case r.Method == http.MethodGet && strings.HasPrefix(path, idrac.SystemPath+"/BootOptions/"):
			ref := strings.TrimPrefix(path, idrac.SystemPath+"/BootOptions/")
			if ref == "Boot0002" {
				writeJSON(map[string]any{"DisplayName": "Virtual Optical Drive"})
				return
			}
			writeJSON(map[string]any{"DisplayName": "Integrated RAID Controller"})

		case r.Method == http.MethodGet && path == f.vmBase():
			members := []map[string]string{
				{"@odata.id": f.vmBase() + "/RemovableDisk"},
				{"@odata.id": f.vmBase() + "/CD"},
			}
			writeJSON(map[string]any{
				"Members@odata.count": len(members), "Members": members,
			})

		// 액션은 리소스 경로에서 조합할 수 있는 관례 경로가 아니라 별도
		// 경로로만 광고합니다. 드라이버가 광고된 Actions.target 대신 조합
		// 경로로 POST하면 여기 도달하지 못해(404) 실패합니다.
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/redfish/v1/VMActions/"):
			rest := strings.TrimPrefix(path, "/redfish/v1/VMActions/")
			parts := strings.SplitN(rest, "/", 2)
			media, ok := f.media[parts[0]]
			if !ok || len(parts) != 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			switch parts[1] {
			case "InsertMedia":
				raw, _ := io.ReadAll(r.Body)
				f.insertBodies = append(f.insertBodies, string(raw))
				var body struct {
					Image string `json:"Image"`
				}
				_ = json.Unmarshal(raw, &body)
				media.inserted = true
				media.image = body.Image
				media.connectedVia = "URI"
				writeJSON(map[string]any{})
			case "EjectMedia":
				if !media.inserted {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				media.inserted = false
				media.image = ""
				media.connectedVia = ""
				writeJSON(map[string]any{})
			default:
				w.WriteHeader(http.StatusNotFound)
			}

		case r.Method == http.MethodGet && strings.HasPrefix(path, f.vmBase()+"/"):
			id := strings.TrimPrefix(path, f.vmBase()+"/")
			media, ok := f.media[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			payload := map[string]any{
				"Id": media.id, "MediaTypes": media.mediaTypes,
				"Inserted": media.inserted, "Image": media.image,
				"ImageName": media.image, "ConnectedVia": media.connectedVia,
				"Actions": map[string]any{
					"#VirtualMedia.EjectMedia": map[string]string{
						"target": "/redfish/v1/VMActions/" + id + "/EjectMedia"},
				},
			}
			if media.insertable {
				payload["Actions"].(map[string]any)["#VirtualMedia.InsertMedia"] = map[string]string{
					"target": "/redfish/v1/VMActions/" + id + "/InsertMedia"}
			}
			writeJSON(payload)

		case path == idrac.AttributesPath:
			if f.noDellAttributes {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPatch {
				var body struct {
					Attributes map[string]string `json:"Attributes"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				for k, v := range body.Attributes {
					f.attributes[k] = v
				}
				writeJSON(map[string]any{})
				return
			}
			f.attributeGets++
			if f.attrErrorStatus != 0 && (f.attrErrorsUntil == 0 || f.attributeGets <= f.attrErrorsUntil) {
				w.WriteHeader(f.attrErrorStatus)
				return
			}
			attrs := map[string]any{}
			for k, v := range f.attributes {
				attrs[k] = v
			}
			// SCP Import가 접수되어 있으면 반영된 값을 보고합니다(비동기 Job
			// 재현): Task를 추적할 때는 Task가 Completed를 보고한 뒤부터,
			// Location이 없을 때는 지정된 속성 조회 횟수 이후부터 적용합니다.
			if f.scpApplied() {
				attrs["ServerBoot.1.FirstBootDevice"] = f.scpDeviceValue
				attrs["ServerBoot.1.BootOnce"] = "Enabled"
			}
			writeJSON(map[string]any{"Attributes": attrs})

		default:
			f.t.Logf("예상하지 못한 요청: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// taskPath는 테스트 서버가 SCP Import 응답 Location으로 알려주는 Task 경로입니다.
const taskPath = "/redfish/v1/TaskService/Tasks/JID_1"

// scpApplied는 SCP Import의 속성 변경이 반영된 시점인지 판단합니다.
func (f *fakeIDRAC9) scpApplied() bool {
	if len(f.scpBuffers) == 0 {
		return false
	}
	if f.noTaskLocation {
		okAttrGets := f.attributeGets
		if f.attrErrorStatus != 0 {
			okAttrGets -= f.attrErrorsUntil
		}
		return okAttrGets > f.scpApplyAfterPolls
	}
	okGets := f.taskGets - f.taskNotFoundUntil
	if f.taskErrorStatus != 0 {
		okGets -= f.taskErrorsUntil
	}
	if f.taskVanishAfter > 0 {
		if f.taskVanishFor == 0 {
			okGets = min(okGets, f.taskVanishAfter)
		} else {
			okGets -= min(max(f.taskGets-f.taskVanishAfter, 0), f.taskVanishFor)
		}
	}
	return !f.taskNeverCompletes && okGets > f.taskPollsUntilDone &&
		(f.taskFinalState == "" || f.taskFinalState == "Completed") &&
		f.taskDellJobState != "Failed" && f.taskDellJobState != "CompletedWithErrors"
}

// bootEnv는 iDRAC9 테스트 서버와 ISO 서버, 로그인된 클라이언트를 준비합니다.
func bootEnv(t *testing.T, fake *fakeIDRAC9) (*redfish.Client, string, func()) {
	t.Helper()
	idracServer := httptest.NewTLSServer(fake.handler())
	isoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	isoURL := isoServer.URL + "/worker2.other.example.com.iso"

	client := redfish.New(strings.TrimPrefix(idracServer.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	if err := client.Login(context.Background(), "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	var once sync.Once
	// 반환하는 정리 함수는 여러 번 불러도 안전합니다. 제한 시간에 걸려
	// 클라이언트가 끊은 요청의 핸들러가 아직 실행 중일 수 있으므로, 카운터를
	// 단언하기 전에 먼저 불러 서버 종료(핸들러 완료 대기)를 끝내는 용도로도
	// 씁니다.
	return client, isoURL, func() {
		once.Do(func() {
			client.Logout()
			idracServer.Close()
			isoServer.Close()
		})
	}
}

func fastWaits() bmc.Waits {
	return bmc.Waits{
		PowerOff: 200 * time.Millisecond, Media: time.Millisecond,
		Attribute: 200 * time.Millisecond, PowerOn: time.Millisecond,
	}
}

func setFastPolls(t *testing.T) {
	t.Helper()
	origPower, origBoot := powerOffPollInterval, bootOncePollInterval
	origTask, origGrace := scpTaskWait, scpTaskNotFoundGrace
	t.Cleanup(func() {
		powerOffPollInterval, bootOncePollInterval = origPower, origBoot
		scpTaskWait, scpTaskNotFoundGrace = origTask, origGrace
	})
	powerOffPollInterval = 10 * time.Millisecond
	bootOncePollInterval = 10 * time.Millisecond
	scpTaskWait = 200 * time.Millisecond
	scpTaskNotFoundGrace = 40 * time.Millisecond
}

// 구형 펌웨어(가상 미디어가 Managers 아래, System 광고 없음) 경로의 전체
// 부팅 절차를 고정합니다. 비동기 SCP Task(조회 1회 뒤 완료)의 추적도
// 포함합니다.
func TestBootPerformsFullSequenceOnOldFirmware(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPollsUntilDone = 1
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}

	// 전원: ForceOff 후 On이어야 합니다.
	if len(fake.resets) != 2 || fake.resets[0] != "ForceOff" || fake.resets[1] != "On" {
		t.Errorf("전원 제어 순서: %v", fake.resets)
	}
	// 이전 ISO는 제거되고 새 ISO가 CD에 마운트되어야 합니다.
	if got := fake.media["CD"].image; got != isoURL {
		t.Errorf("CD에 마운트된 ISO: %q", got)
	}
	// InsertMedia 본문은 현장 스크립트와 같은 최소 형식(Image만)을
	// 유지해야 합니다.
	if len(fake.insertBodies) != 1 {
		t.Fatalf("InsertMedia 호출 수: %d", len(fake.insertBodies))
	}
	var insertBody map[string]any
	if err := json.Unmarshal([]byte(fake.insertBodies[0]), &insertBody); err != nil {
		t.Fatalf("InsertMedia 본문 해석 실패: %v", err)
	}
	if len(insertBody) != 1 || insertBody["Image"] != isoURL {
		t.Errorf("InsertMedia 본문은 Image만 있어야 합니다: %s", fake.insertBodies[0])
	}
	// 부트원스는 SCP Import로 설정되어야 합니다(스크립트의 XML 값 그대로).
	if len(fake.scpBuffers) != 1 {
		t.Fatalf("SCP Import 호출 수: %d", len(fake.scpBuffers))
	}
	for _, want := range []string{
		`Name="ServerBoot.1#BootOnce">Enabled`,
		`Name="ServerBoot.1#FirstBootDevice">VCD-DVD`,
	} {
		if !strings.Contains(fake.scpBuffers[0], want) {
			t.Errorf("ImportBuffer에 %q가 있어야 합니다: %s", want, fake.scpBuffers[0])
		}
	}
	// Import 요청은 HostPowerState=Off를 명시해야 합니다(기본값 On이면
	// iDRAC이 import 끝에 호스트를 먼저 켤 수 있음).
	if len(fake.scpPowerStates) != 1 || fake.scpPowerStates[0] != "Off" {
		t.Errorf("SCP Import의 HostPowerState는 Off여야 합니다: %v", fake.scpPowerStates)
	}
	// 반영 확인은 Location의 Task를 Completed까지 폴링해야 하고(Running 1회
	// 뒤 Completed), 완료 후 결과 속성을 한 번 확인합니다.
	if fake.taskGets < 2 {
		t.Errorf("SCP Task를 완료까지 폴링해야 합니다: 조회 %d회", fake.taskGets)
	}
	if fake.attributeGets != 1 {
		t.Errorf("Task 완료 후 결과 속성을 한 번 확인해야 합니다: 조회 %d회", fake.attributeGets)
	}
	// 전원 ON은 Task가 끝난 뒤여야 합니다: 마지막 Task 조회가 두 번째
	// Reset(On)보다 앞서야 합니다.
	if lastTask, powerOn := taskAndPowerOnIndex(fake); lastTask < 0 || powerOn < 0 || lastTask > powerOn {
		t.Errorf("전원 ON(%d)은 SCP Task 완료 확인(%d) 뒤여야 합니다:\n%s",
			powerOn, lastTask, strings.Join(fake.requestedPaths, "\n"))
	}
	// 부트 순서 보호: 가상 미디어 항목이 제외된 순서가 Settings에 등록되어야 합니다.
	if len(fake.settings) != 1 || fake.settings[0] != "Boot0001" {
		t.Errorf("부트 순서 보호 결과: %v", fake.settings)
	}
	// 폴백: 구형 펌웨어에서는 Managers 아래 컬렉션을 조회해야 합니다.
	joined := strings.Join(fake.requestedPaths, "\n")
	if !strings.Contains(joined, "GET "+ManagerVirtualMediaPath) {
		t.Errorf("Managers 아래 Virtual Media 컬렉션을 조회해야 합니다:\n%s", joined)
	}
	// 액션 POST는 광고된 target으로만 가야 합니다(조합 경로 금지).
	for _, want := range []string{
		"POST /redfish/v1/VMActions/CD/EjectMedia",  // 이전 ISO 제거
		"POST /redfish/v1/VMActions/CD/InsertMedia", // 새 ISO 마운트
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("광고된 액션 target을 사용해야 합니다: want %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "/Actions/VirtualMedia.") {
		t.Errorf("리소스 경로에서 조합한 액션 경로를 사용하면 안 됩니다:\n%s", joined)
	}
	// iDRAC9 드라이버는 Virtual Media 속성(Attached/EncryptEnable)을
	// 변경하지 않아야 합니다 — 검증된 스크립트에 없던 설정 변경이자
	// 암호화 해제 부작용이 있는 단계입니다.
	if strings.Contains(joined, "PATCH "+idrac.AttributesPath) {
		t.Errorf("iDRAC9 드라이버는 DellAttributes를 변경하면 안 됩니다:\n%s", joined)
	}
}

// 신형 펌웨어에서는 System이 광고한 Systems 아래 컬렉션을 사용해야 합니다.
func TestBootUsesSystemAdvertisedVirtualMedia(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.systemsCollection = true
	fake.systemAdvertisesVM = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}
	joined := strings.Join(fake.requestedPaths, "\n")
	if !strings.Contains(joined, "GET "+idrac.SystemPath+"/VirtualMedia") {
		t.Errorf("광고된 Systems 아래 컬렉션을 조회해야 합니다:\n%s", joined)
	}
	if strings.Contains(joined, "GET "+ManagerVirtualMediaPath) {
		t.Errorf("광고가 있으면 Managers 경로로 폴백하면 안 됩니다:\n%s", joined)
	}
}

// DellAttributes가 없는 구형 펌웨어에서도 부팅은 계속되어야 합니다
// (안정성 속성과 부트원스 확인은 경고 후 진행 — 확정된 fail-open 정책).
func TestBootContinuesWithoutDellAttributes(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.noDellAttributes = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("DellAttributes가 없어도 부팅은 계속되어야 합니다: %v", err)
	}
	if len(fake.scpBuffers) != 1 {
		t.Errorf("SCP Import는 수행되어야 합니다: %d", len(fake.scpBuffers))
	}
	if fake.resets[len(fake.resets)-1] != "On" {
		t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
	}
}

// SCP Import 요청 자체가 거부되면 전원을 켜지 않고 중단해야 합니다.
func TestBootFailsWhenSCPImportRejected(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.failSCPImport = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "SCP Import") {
		t.Fatalf("SCP Import 거부는 오류여야 합니다: %v", err)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("부트 설정 실패 후 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// Eject는 삽입된 미디어만 제거하고 전체가 비었는지 검증해야 합니다.
func TestEjectRemovesInsertedMediaOnly(t *testing.T) {
	fake := newFakeIDRAC9(t)
	client, _, done := bootEnv(t, fake)
	defer done()

	if err := Eject(context.Background(), client, time.Millisecond); err != nil {
		t.Fatalf("Eject 실패: %v", err)
	}
	if fake.media["CD"].inserted {
		t.Error("CD의 미디어가 제거되어야 합니다")
	}
	// 빈 장치(RemovableDisk)에 EjectMedia를 호출했다면 테스트 서버가 500을
	// 반환해 실패했을 것이므로, 성공 자체가 Inserted 확인의 증거입니다.
	joined := strings.Join(fake.requestedPaths, "\n")
	if !strings.Contains(joined, "POST /redfish/v1/VMActions/CD/EjectMedia") {
		t.Errorf("광고된 EjectMedia target을 사용해야 합니다:\n%s", joined)
	}
}

// 펌웨어 6.00 이상처럼 Systems 아래 컬렉션은 있지만 System 리소스가
// 참조를 광고하지 않는 경우에도, 표준 경로를 먼저 시도해 찾아야 합니다.
func TestBootFindsSystemsCollectionWithoutAdvertisedLink(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.systemsCollection = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}
	joined := strings.Join(fake.requestedPaths, "\n")
	if !strings.Contains(joined, "GET "+idrac.SystemPath+"/VirtualMedia") {
		t.Errorf("Systems 아래 표준 경로를 시도해야 합니다:\n%s", joined)
	}
	if strings.Contains(joined, "GET "+ManagerVirtualMediaPath) {
		t.Errorf("표준 경로에서 찾았으면 Managers 경로로 넘어가면 안 됩니다:\n%s", joined)
	}
}

// 컬렉션 후보는 HTTP 404일 때만 다음으로 넘어갑니다. 서버 오류처럼 실제
// 문제는 폴백으로 가리지 말고 중단해야 합니다.
func TestBootDoesNotFallBackOnNon404(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.failSystemsCollection = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("404가 아닌 오류는 중단되어야 합니다")
	}
	joined := strings.Join(fake.requestedPaths, "\n")
	if strings.Contains(joined, "GET "+ManagerVirtualMediaPath) {
		t.Errorf("서버 오류(500)에는 Managers 경로로 폴백하면 안 됩니다:\n%s", joined)
	}
	if len(fake.resets) != 0 {
		t.Errorf("장치 목록 단계에서 중단되면 전원을 건드리면 안 됩니다: %v", fake.resets)
	}
}

// Settings 리소스가 없는 펌웨어에서는 부트 순서 보호를 경고 후 건너뛰고
// 부팅은 계속되어야 합니다. ComputerSystem에 직접 PATCH하는 폴백은 문서
// 근거가 없어 수행하지 않습니다.
func TestBootOrderProtectionSkipsWhenSettingsMissing(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.noSettingsResource = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Settings가 없어도 부팅은 계속되어야 합니다: %v", err)
	}
	if len(fake.systemPatches) != 0 {
		t.Errorf("ComputerSystem에 직접 부트 순서를 쓰면 안 됩니다: %v", fake.systemPatches)
	}
	joined := strings.Join(fake.requestedPaths, "\n")
	if !strings.Contains(joined, "PATCH "+idrac.SystemSettingsPath) {
		t.Errorf("Settings 리소스에는 시도해야 합니다:\n%s", joined)
	}
	if fake.resets[len(fake.resets)-1] != "On" {
		t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
	}
}

// SCP Task가 실패(Exception)로 끝나면 전원을 켜지 않고 중단해야 합니다.
func TestBootFailsWhenSCPTaskFails(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPollsUntilDone = 1
	fake.taskFinalState = "Exception"
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "실패했습니다") || !strings.Contains(err.Error(), "Exception") {
		t.Fatalf("SCP Task 실패는 즉시 실패 오류여야 합니다: %v", err)
	}
	// 실패 상태를 보자마자 중단해야 합니다(상한까지 폴링하면 안 됨).
	if fake.taskGets != 2 {
		t.Errorf("Exception을 보고받은 즉시 중단해야 합니다: 조회 %d회", fake.taskGets)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("SCP Task 실패 후 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// TaskState가 Completed여도 Dell JobState가 Failed나 CompletedWithErrors면
// (Dell 참조 스크립트와 같이) 실패로 보고 전원을 켜지 않아야 합니다.
// TaskState 자체가 CompletedWithErrors인 경우도 같습니다.
func TestBootFailsWhenSCPJobReportsErrors(t *testing.T) {
	cases := []struct {
		name, taskState, jobState string
	}{
		{"JobState Failed", "", "Failed"},
		{"JobState CompletedWithErrors", "", "CompletedWithErrors"},
		{"TaskState CompletedWithErrors", "CompletedWithErrors", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setFastPolls(t)
			fake := newFakeIDRAC9(t)
			fake.taskFinalState = tc.taskState
			fake.taskDellJobState = tc.jobState
			client, isoURL, done := bootEnv(t, fake)
			defer done()

			err := Boot(context.Background(), client, bmc.BootRequest{
				ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
			})
			if err == nil || !strings.Contains(err.Error(), "실패했습니다") {
				t.Fatalf("%s는 오류여야 합니다: %v", tc.name, err)
			}
			if fake.taskGets != 1 {
				t.Errorf("실패를 보고받은 즉시 중단해야 합니다: 조회 %d회", fake.taskGets)
			}
			for _, reset := range fake.resets {
				if reset == "On" {
					t.Errorf("실패한 SCP 작업 뒤에 전원을 켜면 안 됩니다: %v", fake.resets)
				}
			}
		})
	}
}

// Task 조회가 401·403처럼 4xx(404 제외)로 거부되면 속성 확인으로 폴백하지
// 말고 즉시 중단해야 합니다(전원 안 켬).
func TestBootFailsWhenSCPTaskQueryRejected(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskErrorStatus = http.StatusUnauthorized
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "조회하지 못했습니다") {
		t.Fatalf("401은 즉시 오류여야 합니다: %v", err)
	}
	if fake.taskGets != 1 {
		t.Errorf("4xx는 재시도 없이 중단해야 합니다: 조회 %d회", fake.taskGets)
	}
	if fake.attributeGets != 0 {
		t.Errorf("4xx에는 속성 확인으로 폴백하면 안 됩니다: 조회 %d회", fake.attributeGets)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("작업 상태를 모르는 채로 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// 일시적인 5xx(작업 처리 중 iDRAC이 내는 503 등)는 상한 안에서 재시도해
// 이후 조회가 성공하면 정상 진행해야 합니다.
func TestBootRetriesSCPTaskQueryOnTransientServerError(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskErrorStatus = http.StatusServiceUnavailable
	fake.taskErrorsUntil = 2
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("일시적 503 뒤에는 정상 진행해야 합니다: %v", err)
	}
	if fake.taskGets < 3 {
		t.Errorf("503 뒤에 재시도해야 합니다: 조회 %d회", fake.taskGets)
	}
	if fake.resets[len(fake.resets)-1] != "On" {
		t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
	}
}

// 5xx가 상한까지 계속되면 작업 상태를 모르는 채로 전원을 켜지 말고
// 중단해야 합니다(속성 확인 폴백 아님).
func TestBootFailsWhenSCPTaskQueryKeepsFailing(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskErrorStatus = http.StatusInternalServerError
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
	if err == nil || !strings.Contains(err.Error(), "조회하지 못했습니다") {
		t.Fatalf("지속되는 5xx는 오류여야 합니다: %v", err)
	}
	if fake.taskGets < 2 {
		t.Errorf("상한까지 재시도해야 합니다: 조회 %d회", fake.taskGets)
	}
	if fake.attributeGets != 0 {
		t.Errorf("5xx에는 속성 확인으로 폴백하면 안 됩니다: 조회 %d회", fake.attributeGets)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("작업 상태를 모르는 채로 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// SCP Task가 상한 안에 끝나지 않으면 — 작업이 진행 중인 채로 전원을 켜면
// 부트원스가 반영되기 전에 부팅될 수 있으므로 — 전원을 켜지 않고 중단해야
// 합니다(fail-open이 아님).
func TestBootFailsWhenSCPTaskDoesNotFinish(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskNeverCompletes = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
	if err == nil || !strings.Contains(err.Error(), "끝나지 않았습니다") {
		t.Fatalf("SCP Task 시간 초과는 오류여야 합니다: %v", err)
	}
	if fake.taskGets < 2 {
		t.Errorf("상한까지 Task를 폴링해야 합니다: 조회 %d회", fake.taskGets)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("SCP Task 미완료 상태에서 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// SCP Import 응답에 Location이 없는 펌웨어에서는 결과 속성 폴링으로
// 반영을 확인하고(비동기 반영 뒤 확인 성공) 진행해야 합니다.
func TestBootFallsBackToAttributePollingWithoutTaskLocation(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.noTaskLocation = true
	fake.scpApplyAfterPolls = 1
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}
	if fake.taskGets != 0 {
		t.Errorf("Location이 없으면 Task를 조회할 수 없습니다: 조회 %d회", fake.taskGets)
	}
	if fake.attributeGets < 2 {
		t.Errorf("부트 설정 반영을 속성 폴링으로 확인해야 합니다: 조회 %d회", fake.attributeGets)
	}
	if fake.resets[len(fake.resets)-1] != "On" {
		t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
	}
}

// Location은 있지만 Task 리소스가 없는(HTTP 404) 펌웨어에서는 판정
// 불가로 보고 속성 확인(fail-open)으로 폴백해 부팅을 계속해야 합니다.
// 404만 폴백 대상입니다(다른 오류는 위 테스트들).
func TestBootFallsBackWhenSCPTaskUnavailable(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskUnavailable = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Task를 조회할 수 없어도 속성 확인으로 폴백해 계속되어야 합니다: %v", err)
	}
	done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
	if fake.taskGets < 2 {
		t.Errorf("404는 유예 시간 동안 다시 조회한 뒤에 미지원으로 판단해야 합니다: 조회 %d회", fake.taskGets)
	}
	if fake.attributeGets == 0 {
		t.Error("Task 판정 불가 시 결과 속성을 확인해야 합니다")
	}
	if fake.resets[len(fake.resets)-1] != "On" {
		t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
	}
}

// SCP 접수 직후 Task 조회가 404여도 곧바로 미지원으로 보지 말고 유예
// 시간 동안 다시 조회해, 작업이 나타나면 Task 추적으로 진행해야 합니다
// (Dell 참조 스크립트도 접수 후 잠시 기다린 뒤 조회).
func TestBootWaitsForSCPTaskToAppear(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskNotFoundUntil = 2
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("유예 시간 안에 나타난 Task는 추적해야 합니다: %v", err)
	}
	if fake.taskGets < 3 {
		t.Errorf("첫 404 뒤에 다시 조회해야 합니다: 조회 %d회", fake.taskGets)
	}
	// Task 추적으로 끝났으므로 속성 확인은 완료 후 1회뿐이어야 합니다
	// (속성 폴링 폴백으로 넘어가면 안 됨).
	if fake.attributeGets != 1 {
		t.Errorf("Task를 추적했으면 속성 폴링으로 폴백하면 안 됩니다: 조회 %d회", fake.attributeGets)
	}
}

// 한 번 조회된 Task가 이후 계속 404가 되면 "미지원 펌웨어"로 재분류해
// 속성 폴백(DellAttributes도 404면 fail-open)으로 흐르면 안 됩니다. 작업
// 완료를 확인하지 못한 것이므로 오류로 중단하고 전원을 켜지 않아야 합니다.
func TestBootFailsWhenSeenSCPTaskDisappears(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPollsUntilDone = 5 // 첫 조회는 Running
	fake.taskVanishAfter = 1    // 이후 계속 404
	fake.noDellAttributes = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
	if err == nil || !strings.Contains(err.Error(), "조회하지 못했습니다") {
		t.Fatalf("조회되던 Task의 지속 404는 오류여야 합니다: %v", err)
	}
	if fake.attributeGets != 0 {
		t.Errorf("미지원으로 재분류해 속성 폴백으로 가면 안 됩니다: 속성 조회 %d회", fake.attributeGets)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("작업 완료를 확인하지 못한 채 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// 조회되던 Task가 일시적으로 404였다가 다시 나타나 완료되면 정상 진행해야
// 합니다.
func TestBootRecoversWhenSeenSCPTaskReappears(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPollsUntilDone = 1 // 첫 조회 Running, 그다음 정상 조회부터 Completed
	fake.taskVanishAfter = 1    // 두 번째·세 번째 조회는 404
	fake.taskVanishFor = 2
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("일시 404 뒤 다시 나타난 Task는 정상 진행해야 합니다: %v", err)
	}
	if fake.taskGets < 4 {
		t.Errorf("404 뒤에도 Task를 계속 조회해야 합니다: 조회 %d회", fake.taskGets)
	}
	if lastTask, powerOn := taskAndPowerOnIndex(fake); lastTask < 0 || powerOn < 0 || lastTask > powerOn {
		t.Errorf("전원 ON(%d)은 Task 완료 확인(%d) 뒤여야 합니다", powerOn, lastTask)
	}
}

// 속성 폴링 폴백(Location 없음)의 조회 오류도 종류별로 다뤄야 합니다:
// 401 같은 4xx는 즉시 중단, 일시적 5xx는 재시도, 지속되는 5xx는 중단.
// (404는 TestBootContinuesWithoutDellAttributes — 유일한 fail-open.)
func TestBootAttributeFallbackClassifiesQueryErrors(t *testing.T) {
	t.Run("401은 즉시 중단", func(t *testing.T) {
		setFastPolls(t)
		fake := newFakeIDRAC9(t)
		fake.noTaskLocation = true
		fake.attrErrorStatus = http.StatusUnauthorized
		client, isoURL, done := bootEnv(t, fake)
		defer done()

		err := Boot(context.Background(), client, bmc.BootRequest{
			ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
		})
		if err == nil || !strings.Contains(err.Error(), "확인하지 못했습니다") {
			t.Fatalf("401은 오류여야 합니다: %v", err)
		}
		if fake.attributeGets != 1 {
			t.Errorf("4xx는 재시도 없이 중단해야 합니다: 조회 %d회", fake.attributeGets)
		}
		for _, reset := range fake.resets {
			if reset == "On" {
				t.Errorf("설정 상태를 모르는 채로 전원을 켜면 안 됩니다: %v", fake.resets)
			}
		}
	})
	t.Run("일시적 503은 재시도", func(t *testing.T) {
		setFastPolls(t)
		fake := newFakeIDRAC9(t)
		fake.noTaskLocation = true
		fake.attrErrorStatus = http.StatusServiceUnavailable
		fake.attrErrorsUntil = 2
		client, isoURL, done := bootEnv(t, fake)
		defer done()

		if err := Boot(context.Background(), client, bmc.BootRequest{
			ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
		}); err != nil {
			t.Fatalf("일시적 503 뒤에는 정상 진행해야 합니다: %v", err)
		}
		if fake.attributeGets < 3 {
			t.Errorf("503 뒤에 재시도해야 합니다: 조회 %d회", fake.attributeGets)
		}
		if fake.resets[len(fake.resets)-1] != "On" {
			t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
		}
	})
	t.Run("지속되는 500은 중단", func(t *testing.T) {
		setFastPolls(t)
		fake := newFakeIDRAC9(t)
		fake.noTaskLocation = true
		fake.attrErrorStatus = http.StatusInternalServerError
		client, isoURL, done := bootEnv(t, fake)
		defer done()

		err := Boot(context.Background(), client, bmc.BootRequest{
			ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
		})
		done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
		if err == nil || !strings.Contains(err.Error(), "확인하지 못했습니다") {
			t.Fatalf("지속되는 5xx는 오류여야 합니다: %v", err)
		}
		if fake.attributeGets < 2 {
			t.Errorf("제한 시간까지 재시도해야 합니다: 조회 %d회", fake.attributeGets)
		}
		for _, reset := range fake.resets {
			if reset == "On" {
				t.Errorf("설정 상태를 모르는 채로 전원을 켜면 안 됩니다: %v", fake.resets)
			}
		}
	})
}

// iDRAC9 6.10의 알려진 결함(Dell 릴리스 노트): SCP import의 TaskState가
// 아직 진행 중인데 Completed로 보고되고 PercentComplete는 43. Dell의
// 우회책대로 진행률이 100이 될 때까지 기다린 뒤에만 전원을 켜야 합니다.
func TestBootWaitsForSCPTaskPercentComplete(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPercentLagPolls = 2
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}
	// Completed+43%가 2회 → 100%가 3번째 조회이므로 최소 3회.
	if fake.taskGets < 3 {
		t.Errorf("PercentComplete가 100이 될 때까지 기다려야 합니다: 조회 %d회", fake.taskGets)
	}
	if lastTask, powerOn := taskAndPowerOnIndex(fake); lastTask < 0 || powerOn < 0 || lastTask > powerOn {
		t.Errorf("전원 ON(%d)은 100%% 확인(%d) 뒤여야 합니다", powerOn, lastTask)
	}
}

// PercentComplete가 상한까지 100에 이르지 않으면 Completed 표시만 믿고
// 전원을 켜면 안 됩니다.
func TestBootFailsWhenSCPTaskNeverReaches100(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPercentLagPolls = -1
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
	if err == nil || !strings.Contains(err.Error(), "끝나지 않았습니다") {
		t.Fatalf("진행률 100 미만의 Completed는 완료로 보면 안 됩니다: %v", err)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// TaskStatus는 Health 값(OK/Warning/Critical)입니다. Completed라도
// Critical이면 실패, Warning은 경고만 남기고 성공입니다.
func TestBootHonorsSCPTaskStatusHealth(t *testing.T) {
	t.Run("Critical은 실패", func(t *testing.T) {
		setFastPolls(t)
		fake := newFakeIDRAC9(t)
		fake.taskStatus = "Critical"
		client, isoURL, done := bootEnv(t, fake)
		defer done()

		err := Boot(context.Background(), client, bmc.BootRequest{
			ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
		})
		if err == nil || !strings.Contains(err.Error(), "실패했습니다") || !strings.Contains(err.Error(), "Critical") {
			t.Fatalf("TaskStatus Critical은 오류여야 합니다: %v", err)
		}
		if fake.taskGets != 1 {
			t.Errorf("즉시 중단해야 합니다: 조회 %d회", fake.taskGets)
		}
		for _, reset := range fake.resets {
			if reset == "On" {
				t.Errorf("전원을 켜면 안 됩니다: %v", fake.resets)
			}
		}
	})
	t.Run("Warning은 진행", func(t *testing.T) {
		setFastPolls(t)
		fake := newFakeIDRAC9(t)
		fake.taskStatus = "Warning"
		client, isoURL, done := bootEnv(t, fake)
		defer done()

		if err := Boot(context.Background(), client, bmc.BootRequest{
			ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
		}); err != nil {
			t.Fatalf("TaskStatus Warning은 진행해야 합니다: %v", err)
		}
		if fake.resets[len(fake.resets)-1] != "On" {
			t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
		}
	})
}

// 속성 폴링 폴백에서 DellAttributes 조회 자체가 제한 시간에 걸려 끝나면
// (느린 iDRAC) 값을 읽지 못한 것이므로 fail-open이 아니라 중단해야 합니다.
func TestBootAttributeFallbackFailsWhenQueryTimesOut(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.noTaskLocation = true
	fake.attrDelay = 500 * time.Millisecond // Attribute 대기(200ms)보다 김
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	done() // 제한 시간에 걸린 요청의 핸들러까지 끝낸 뒤 단언합니다.
	if err == nil || !strings.Contains(err.Error(), "확인하지 못했습니다") {
		t.Fatalf("제한 시간에 걸린 조회는 오류여야 합니다: %v", err)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("값을 읽지 못한 채로 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// taskAndPowerOnIndex는 요청 순서에서 마지막 Task 조회와 두 번째
// Reset(전원 ON)의 위치입니다(없으면 -1). 전원 ON이 Task 완료 확인 뒤인지
// 단언할 때 씁니다.
func taskAndPowerOnIndex(f *fakeIDRAC9) (lastTask, powerOn int) {
	return lastIndex(f.requestedPaths, "GET "+taskPath),
		nthIndex(f.requestedPaths, "POST "+idrac.SystemPath+"/Actions/ComputerSystem.Reset", 2)
}

// SYS043(변경 없음) + TaskStatus Critical + 진행률 43%가 함께 오면
// (iDRAC9 6.10 결함) 진행률 판정이 먼저이므로 즉시 성공하지 않고 100%가
// 될 때까지 기다린 뒤에만, 그때의 SYS043로 성공 처리하고 전원을 켜야
// 합니다. 진행률 판정이 SYS043보다 앞서지 않으면 진행 중 서버를 켜게 됩니다.
func TestBootWaitsForPercentBeforeSYS043Success(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPollsUntilDone = 0  // 첫 조회부터 Completed로 보고
	fake.taskPercentLagPolls = 2 // 처음 두 번은 43%
	fake.taskMessageID = "IDRAC.2.8.SYS043"
	fake.taskStatus = "Critical"
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("100%% 도달 후 SYS043은 성공이어야 합니다: %v", err)
	}
	// 43%인 동안(2회)은 전원을 켜지 않고 폴링해야 합니다.
	if fake.taskGets < 3 {
		t.Errorf("진행률 100%% 전까지 기다려야 합니다: 조회 %d회", fake.taskGets)
	}
	if lastTask, powerOn := taskAndPowerOnIndex(fake); lastTask < 0 || powerOn < 0 || lastTask > powerOn {
		t.Errorf("전원 ON(%d)은 100%% 확인(%d) 뒤여야 합니다", powerOn, lastTask)
	}
}

// 진행률이 해석 불가한 값(문자열 "43%")으로 계속 오면 완료로 보지 않고
// 상한까지 기다린 뒤 중단해야 합니다(정보 없음으로 처리해 성공하면 안 됩니다).
// 여러 잘못된 표기의 파싱 자체는 TestPercentParsing이 표로 고정합니다.
func TestBootFailsWhenPercentCompleteInvalid(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPercentInvalid = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	})
	done()
	if err == nil || !strings.Contains(err.Error(), "끝나지 않았습니다") {
		t.Fatalf("해석 불가한 진행률은 완료로 보면 안 됩니다: %v", err)
	}
	for _, reset := range fake.resets {
		if reset == "On" {
			t.Errorf("완료를 확인하지 못한 채 전원을 켜면 안 됩니다: %v", fake.resets)
		}
	}
}

// 이미 꺼진 서버(이전 실행이 SCP 단계에서 중단된 뒤 재실행 등)에는
// ForceOff를 보내지 않아야 합니다. iDRAC9는 꺼진 서버에 대한 끄기 요청을
// 거부할 수 있어 그대로 보내면 재실행이 막힙니다.
func TestBootSkipsForceOffWhenAlreadyOff(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.powerState = "Off"
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("꺼진 서버의 재실행은 성공해야 합니다: %v", err)
	}
	if len(fake.resets) != 1 || fake.resets[0] != "On" {
		t.Errorf("꺼진 서버에는 On만 보내야 합니다: %v", fake.resets)
	}
}

// 펌웨어가 HostPowerState=Off를 무시하고 SCP Import 끝에 호스트를 켜
// 버리면, 켜기 요청(거부될 수 있음) 대신 경고 후 진행해야 합니다.
func TestBootSkipsPowerOnWhenSCPAlreadyPoweredOn(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.scpPowersOn = true
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("이미 켜진 서버에 On을 보내 실패하면 안 됩니다: %v", err)
	}
	if len(fake.resets) != 1 || fake.resets[0] != "ForceOff" {
		t.Errorf("켜져 있으면 On을 보내지 않아야 합니다: %v", fake.resets)
	}
}

// 이전 실행이 남긴 원타임 부트 설정이 이미 원하는 값이면 iDRAC은 "적용할
// 변경 없음"(SYS043)으로 완료를 보고합니다. TaskStatus가 Critical이어도
// 결과 상태는 원하는 상태이므로 성공으로 진행해야 합니다.
func TestBootTreatsSCPNoChangeAsSuccess(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskMessageID = "IDRAC.2.8.SYS043"
	fake.taskStatus = "Critical"
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("SYS043(변경 없음)은 성공이어야 합니다: %v", err)
	}
	if fake.resets[len(fake.resets)-1] != "On" {
		t.Errorf("전원이 켜져야 합니다: %v", fake.resets)
	}
}

// PercentComplete가 문자열로 오는 펌웨어에서도 작업 조회가 실패하지 않고
// 완료를 인식해야 합니다.
func TestBootAcceptsPercentCompleteAsString(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.taskPercentAsString = true
	fake.taskPercentLagPolls = 1
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("문자열 진행률도 해석해야 합니다: %v", err)
	}
	if fake.taskGets < 2 {
		t.Errorf("문자열 43도 100 미만으로 보고 기다려야 합니다: 조회 %d회", fake.taskGets)
	}
}

// TestPercentParsing은 PercentComplete 파서와 완료 허용 판정을 값별로
// 고정합니다. 잘못된 값을 "정보 없음"으로 처리해 완료를 허용하면 안 됩니다.
func TestPercentParsing(t *testing.T) {
	// wrap은 task JSON에서 PercentComplete를 디코딩한 결과를 반환합니다.
	// 필드 생략과 JSON null은 포인터가 nil이어야 합니다.
	cases := []struct {
		raw       string // PercentComplete 원시 JSON. "omit"이면 필드 생략.
		wantNil   bool   // 포인터가 nil(정보 없음)이어야 하는가
		wantValue percent
		permit    bool // permitsCompletion 기대(nil이 아닐 때만 의미)
	}{
		{raw: "omit", wantNil: true},
		{raw: "null", wantNil: true},
		{raw: "100", wantValue: 100, permit: true},
		{raw: `"100"`, wantValue: 100, permit: true},
		{raw: "0", wantValue: 0, permit: false},
		{raw: "43", wantValue: 43, permit: false},
		{raw: `"43"`, wantValue: 43, permit: false},
		{raw: `""`, wantValue: pctInvalid, permit: false},
		{raw: `"null"`, wantValue: pctInvalid, permit: false},
		{raw: `"unknown"`, wantValue: pctInvalid, permit: false},
		{raw: `"43%"`, wantValue: pctInvalid, permit: false},
		{raw: "-5", wantValue: pctInvalid, permit: false},
		{raw: "101", wantValue: pctInvalid, permit: false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			body := `{"TaskState":"Completed"}`
			if tc.raw != "omit" {
				body = `{"TaskState":"Completed","PercentComplete":` + tc.raw + `}`
			}
			var task task
			if err := json.Unmarshal([]byte(body), &task); err != nil {
				t.Fatalf("Unmarshal 실패: %v", err)
			}
			if tc.wantNil {
				if task.PercentComplete != nil {
					t.Fatalf("정보 없음이어야 합니다(nil): %v", *task.PercentComplete)
				}
				return
			}
			if task.PercentComplete == nil {
				t.Fatalf("nil이면 안 됩니다: raw=%s", tc.raw)
			}
			if *task.PercentComplete != tc.wantValue {
				t.Errorf("값: got %d want %d", *task.PercentComplete, tc.wantValue)
			}
			if got := task.PercentComplete.permitsCompletion(); got != tc.permit {
				t.Errorf("permitsCompletion: got %t want %t", got, tc.permit)
			}
		})
	}
}

// lastIndex는 want와 같은 마지막 항목의 위치입니다(없으면 -1).
func lastIndex(items []string, want string) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i] == want {
			return i
		}
	}
	return -1
}

// nthIndex는 want와 같은 n번째(1부터) 항목의 위치입니다(없으면 -1).
func nthIndex(items []string, want string, n int) int {
	for i, item := range items {
		if item == want {
			n--
			if n == 0 {
				return i
			}
		}
	}
	return -1
}

// iDRAC이 FirstBootDevice를 vCD-DVD로 보고하는 펌웨어에서도 반영 확인이
// 첫 조회에서 성공해야 합니다(대소문자 차이로 제한 시간까지 폴링하면 안 됨).
// 속성 폴링 경로(Location 없음)로 확인합니다.
func TestBootOnceVerificationAcceptsLowercaseDeviceValue(t *testing.T) {
	setFastPolls(t)
	fake := newFakeIDRAC9(t)
	fake.noTaskLocation = true
	fake.scpDeviceValue = "vCD-DVD"
	client, isoURL, done := bootEnv(t, fake)
	defer done()

	if err := Boot(context.Background(), client, bmc.BootRequest{
		ISOURL: isoURL, Waits: fastWaits(), HTTPTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}
	if fake.attributeGets != 1 {
		t.Errorf("vCD-DVD 표기도 첫 조회에서 확인되어야 합니다: 조회 %d회", fake.attributeGets)
	}
}
