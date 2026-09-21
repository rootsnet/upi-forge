package idrac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/redfish"
)

// fakeIDRAC은 iDRAC10 Redfish의 최소 동작을 재현하는 테스트 서버입니다.
// Boot()의 11단계가 실제 HTTP 흐름에서 올바른 순서로 동작하는지 확인합니다.
type fakeIDRAC struct {
	t          *testing.T
	attributes map[string]string
	media      map[string]*fakeMedia
	bootOrder  []string
	settings   []string // Settings 리소스에 기록된 새 BootOrder
	resets     []string
	powerState string

	// failBootOrderPatch가 true면 Settings PATCH를 거부합니다.
	// BootOrder 쓰기를 지원하지 않거나 잠근 장비를 재현합니다.
	failBootOrderPatch bool

	// pendingPowerStates가 있으면 System GET마다 앞에서부터 하나씩
	// PowerState로 보고합니다. 전원 전이가 느린 장비를 재현합니다.
	pendingPowerStates []string
	// stuckPower가 true면 ForceOff를 받아도 전원이 꺼지지 않습니다.
	stuckPower bool
	// slowSystemGets가 남아 있는 동안 System GET이 5초간(또는 요청 취소까지)
	// 응답하지 않습니다. 응답이 멈춘 BMC를 재현합니다.
	slowSystemGets int
	// failNthSystemGet가 0이 아니면 그 번째(서버에 도달한 순서 기준)
	// System GET에 500을 반환합니다.
	systemGets       int
	failNthSystemGet int
	// failBootOptionGets에 있는 부트 항목의 조회는 500을 반환합니다.
	failBootOptionGets map[string]bool
}

type fakeMedia struct {
	id             string
	mediaTypes     []string
	insertable     bool
	inserted       bool
	image          string
	connectedVia   string
	writeProtected bool
}

func newFakeIDRAC(t *testing.T) *fakeIDRAC {
	return &fakeIDRAC{
		t:          t,
		attributes: map[string]string{"ServerBoot.1.FirstBootDevice": "Normal"},
		powerState: "On",
		// 컬렉션 순서를 뒤집어 두고, 구현이 정렬해서 1번을 고르는지 확인합니다.
		media: map[string]*fakeMedia{
			"1": {id: "1", mediaTypes: []string{"CD", "DVD"}, insertable: true},
			"2": {id: "2", mediaTypes: []string{"CD", "DVD"}, insertable: true,
				inserted: true, image: "http://old/stale.iso", connectedVia: "URI"},
			"3": {id: "3", mediaTypes: []string{"USBStick"}},
		},
		bootOrder: []string{"Boot0001", "Boot0002"},
	}
}

func (f *fakeIDRAC) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
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

		case r.Method == http.MethodPost && path == SystemPath+"/Actions/ComputerSystem.Reset":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.resets = append(f.resets, body["ResetType"])
			switch body["ResetType"] {
			case "ForceOff":
				if !f.stuckPower {
					f.powerState = "Off"
				}
			case "On":
				f.powerState = "On"
			}
			writeJSON(map[string]any{})

		case r.Method == http.MethodGet && path == SystemPath:
			f.systemGets++
			if f.failNthSystemGet != 0 && f.systemGets == f.failNthSystemGet {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if f.slowSystemGets > 0 {
				f.slowSystemGets--
				select {
				case <-r.Context().Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
			state := f.powerState
			if len(f.pendingPowerStates) > 0 {
				state = f.pendingPowerStates[0]
				f.pendingPowerStates = f.pendingPowerStates[1:]
			}
			writeJSON(map[string]any{
				"Id": "System.Embedded.1", "Model": "PowerEdge R670",
				"PowerState": state,
				"Status":     map[string]string{"Health": "OK"},
				"Boot":       map[string]any{"BootOrder": f.bootOrder},
			})

		case r.Method == http.MethodPatch && path == SystemSettingsPath:
			if f.failBootOrderPatch {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Base.1.18.PropertyNotWritable"}}`))
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

		case r.Method == http.MethodGet && strings.HasPrefix(path, SystemPath+"/BootOptions/"):
			ref := strings.TrimPrefix(path, SystemPath+"/BootOptions/")
			if f.failBootOptionGets[ref] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if ref == "Boot0002" {
				writeJSON(map[string]any{
					"DisplayName": "Virtual Optical Drive 1.1",
					"RelatedItem": []map[string]string{
						{"@odata.id": SystemPath + "/VirtualMedia/1"},
					},
				})
				return
			}
			writeJSON(map[string]any{"DisplayName": "PCIe SSD in Slot 1"})

		case r.Method == http.MethodGet && path == SystemPath+"/VirtualMedia":
			// 순서를 일부러 뒤집어 반환합니다.
			writeJSON(map[string]any{
				"Members@odata.count": 3,
				"Members": []map[string]string{
					{"@odata.id": SystemPath + "/VirtualMedia/2"},
					{"@odata.id": SystemPath + "/VirtualMedia/3"},
					{"@odata.id": SystemPath + "/VirtualMedia/1"},
				},
			})

		case strings.HasPrefix(path, SystemPath+"/VirtualMedia/"):
			rest := strings.TrimPrefix(path, SystemPath+"/VirtualMedia/")
			id := strings.SplitN(rest, "/", 2)[0]
			media, ok := f.media[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			switch {
			case strings.HasSuffix(path, "/Actions/VirtualMedia.InsertMedia"):
				var body struct {
					Image          string `json:"Image"`
					Inserted       bool   `json:"Inserted"`
					WriteProtected bool   `json:"WriteProtected"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				media.inserted = body.Inserted
				media.image = body.Image
				media.writeProtected = body.WriteProtected
				media.connectedVia = "URI"
				writeJSON(map[string]any{})
			case strings.HasSuffix(path, "/Actions/VirtualMedia.EjectMedia"):
				if !media.inserted {
					// 미디어가 없는 장치의 eject는 실패시킵니다.
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				media.inserted = false
				media.image = ""
				media.connectedVia = ""
				writeJSON(map[string]any{})
			default:
				payload := map[string]any{
					"Id": media.id, "MediaTypes": media.mediaTypes,
					"Inserted": media.inserted, "Image": media.image,
					"ImageName": media.image, "ConnectedVia": media.connectedVia,
					"WriteProtected": media.writeProtected,
				}
				if media.insertable {
					payload["Actions"] = map[string]any{
						"#VirtualMedia.InsertMedia": map[string]string{
							"target": path + "/Actions/VirtualMedia.InsertMedia"},
					}
				}
				writeJSON(payload)
			}

		case path == AttributesPath:
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
			attrs := map[string]any{}
			for k, v := range f.attributes {
				attrs[k] = v
			}
			writeJSON(map[string]any{"Attributes": attrs})

		default:
			f.t.Logf("예상하지 못한 요청: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestBootPerformsFullSequence(t *testing.T) {
	fake := newFakeIDRAC(t)
	idracServer := httptest.NewTLSServer(fake.handler())
	defer idracServer.Close()

	isoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer isoServer.Close()
	isoURL := isoServer.URL + "/worker2.other.example.com.iso"

	client := redfish.New(strings.TrimPrefix(idracServer.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := client.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer client.Logout()

	err := Boot(ctx, client, bmc.BootRequest{
		ISOURL:      isoURL,
		Waits:       bmc.Waits{}, // 테스트에서는 대기하지 않습니다.
		HTTPTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}

	// 4, 10단계: 전원을 껐다가 다시 켜야 합니다.
	if len(fake.resets) != 2 || fake.resets[0] != "ForceOff" || fake.resets[1] != "On" {
		t.Errorf("전원 제어 순서가 다릅니다: %v", fake.resets)
	}

	// 3단계: 정렬 후 첫 번째 사용 가능 장치(1번)를 선택해야 합니다.
	if !fake.media["1"].inserted || fake.media["1"].image != isoURL {
		t.Errorf("1번 장치에 ISO가 마운트되지 않았습니다: %+v", fake.media["1"])
	}
	if !fake.media["1"].writeProtected {
		t.Error("ISO는 WriteProtected로 마운트해야 합니다")
	}

	// 5단계: 이미 물려 있던 2번 장치의 미디어는 제거되어야 합니다.
	if fake.media["2"].inserted {
		t.Errorf("기존 미디어가 제거되지 않았습니다: %+v", fake.media["2"])
	}

	// 5-1단계: 안정성 속성
	if fake.attributes["VirtualMedia.1.Attached"] != "Attached" {
		t.Errorf("Attached 속성이 설정되지 않았습니다: %v", fake.attributes)
	}
	if fake.attributes["VirtualMedia.1.EncryptEnable"] != "Disabled" {
		t.Errorf("EncryptEnable 속성이 설정되지 않았습니다: %v", fake.attributes)
	}

	// 5-2단계: 부트 순서에서 가상 미디어 항목만 빠져야 합니다.
	if len(fake.settings) != 1 || fake.settings[0] != "Boot0001" {
		t.Errorf("부트 순서가 예상과 다릅니다: %v", fake.settings)
	}

	// 8단계: Dell 전용 원타임 부트 속성 세 개
	want := map[string]string{
		"ServerBoot.1.FirstBootDevice": "VCD-DVD",
		"ServerBoot.1.BootOnce":        "Enabled",
		"VirtualMedia.1.BootOnce":      "Enabled",
	}
	for key, value := range want {
		if fake.attributes[key] != value {
			t.Errorf("%s: got=%q want=%q", key, fake.attributes[key], value)
		}
	}
}

// bootThrough는 테스트 서버에 로그인해 Boot를 실행하는 공용 절차입니다.
func bootThrough(t *testing.T, fake *fakeIDRAC, waits bmc.Waits) error {
	t.Helper()
	idracServer := httptest.NewTLSServer(fake.handler())
	t.Cleanup(idracServer.Close)
	isoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	t.Cleanup(isoServer.Close)

	client := redfish.New(strings.TrimPrefix(idracServer.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := client.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	t.Cleanup(client.Logout)

	return Boot(ctx, client, bmc.BootRequest{
		ISOURL:      isoServer.URL + "/worker1.iso",
		Waits:       waits,
		HTTPTimeout: 5 * time.Second,
	})
}

// 전원이 늦게 꺼지는 장비: PowerState 폴링이 Off를 확인하는 즉시 다음
// 단계로 진행해야 합니다. 폴링을 고정 대기로 되돌리면 제한 시간을 다
// 기다리게 되어 시간 상한 검사가 실패합니다.
func TestBootPollsPowerOffAndProceedsEarly(t *testing.T) {
	orig := powerOffPollInterval
	powerOffPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { powerOffPollInterval = orig })

	fake := newFakeIDRAC(t)
	// ForceOff 뒤 두 번의 조회까지는 전이 중으로 보고합니다.
	fake.pendingPowerStates = []string{"PoweringOff", "PoweringOff"}

	start := time.Now()
	if err := bootThrough(t, fake, bmc.Waits{PowerOff: 30 * time.Second}); err != nil {
		t.Fatalf("Boot 실패: %v", err)
	}
	// 정상 경로는 수십 ms, 폴링을 고정 대기로 되돌리는 회귀는 30초입니다.
	// 상한은 느린 CI를 감안해 회귀값의 절반 이하로 여유를 둡니다.
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Errorf("Off 확인 후 즉시 진행해야 합니다: %s 걸림", elapsed)
	}
	if len(fake.resets) != 2 || fake.resets[1] != "On" {
		t.Errorf("흐름이 끝까지 진행되어야 합니다: %v", fake.resets)
	}
}

// 응답이 멈춘 BMC: 상태 조회(GET) 자체도 제한 시간의 적용을 받아야
// 합니다. 조회에 deadline을 걸지 않으면 HTTP 제한 시간(이 테스트에서는
// 5초)까지 붙들려 powerOffWaitSeconds가 상한 역할을 하지 못합니다.
func TestBootPowerOffLimitBoundsSlowStatusGet(t *testing.T) {
	orig := powerOffPollInterval
	powerOffPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { powerOffPollInterval = orig })

	fake := newFakeIDRAC(t)
	fake.slowSystemGets = 1 // 전원 확인 GET이 응답하지 않음

	start := time.Now()
	if err := bootThrough(t, fake, bmc.Waits{PowerOff: 50 * time.Millisecond}); err != nil {
		t.Fatalf("조회가 멈춰도 경고 후 진행해야 합니다: %v", err)
	}
	// 정상 경로는 수십 ms, GET에 deadline을 걸지 않는 회귀는 5초(테스트
	// 서버의 지연)입니다. 상한은 느린 CI를 감안해 그 사이에 둡니다.
	if elapsed := time.Since(start); elapsed >= 4*time.Second {
		t.Errorf("전원 확인 단계가 제한 시간의 상한을 넘겼습니다: %s 걸림", elapsed)
	}
	if len(fake.resets) != 2 || fake.resets[1] != "On" {
		t.Errorf("흐름이 끝까지 진행되어야 합니다: %v", fake.resets)
	}
}

// 제한 시간 안에 전원이 꺼지지 않는 장비: 실패 대신 경고 후 진행해야
// 합니다(확정된 운영 정책 fail-open — 고정 대기만 하던 동작과 최악의
// 경우가 같아야 합니다).
func TestBootPowerOffTimeoutStillProceeds(t *testing.T) {
	orig := powerOffPollInterval
	powerOffPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { powerOffPollInterval = orig })

	fake := newFakeIDRAC(t)
	fake.stuckPower = true // ForceOff를 받아도 계속 On

	if err := bootThrough(t, fake, bmc.Waits{PowerOff: 30 * time.Millisecond}); err != nil {
		t.Fatalf("전원이 안 꺼져도 경고 후 진행해야 합니다: %v", err)
	}
	if len(fake.resets) != 2 || fake.resets[1] != "On" {
		t.Errorf("흐름이 끝까지 진행되어야 합니다: %v", fake.resets)
	}
}

func TestBootFailsWhenISOUnreachable(t *testing.T) {
	fake := newFakeIDRAC(t)
	idracServer := httptest.NewTLSServer(fake.handler())
	defer idracServer.Close()

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer missing.Close()

	client := redfish.New(strings.TrimPrefix(idracServer.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := client.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer client.Logout()

	err := Boot(ctx, client, bmc.BootRequest{ISOURL: missing.URL + "/x.iso", HTTPTimeout: 5 * time.Second})
	if err == nil {
		t.Fatal("ISO에 접근할 수 없으면 실패해야 합니다")
	}
	// ISO 확인 단계에서 멈춰야 하므로 서버 전원은 건드리지 않아야 합니다.
	if len(fake.resets) != 0 {
		t.Errorf("ISO 확인 실패 시 전원을 제어하면 안 됩니다: %v", fake.resets)
	}
}

func TestEjectRemovesAllMedia(t *testing.T) {
	fake := newFakeIDRAC(t)
	fake.media["1"].inserted = true
	fake.media["1"].image = "http://web/a.iso"
	server := httptest.NewTLSServer(fake.handler())
	defer server.Close()

	client := redfish.New(strings.TrimPrefix(server.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := client.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer client.Logout()

	if err := Eject(ctx, client, 0); err != nil {
		t.Fatalf("Eject 실패: %v", err)
	}
	for id, media := range fake.media {
		if media.inserted {
			t.Errorf("장치 %s의 미디어가 남아 있습니다", id)
		}
	}
}

// TestBootContinuesWhenBootOrderPatchRejected는 의도된 fail-open 동작을 고정합니다.
//
// 부트 순서에서 Virtual CD/DVD를 제외하는 단계는 보호 장치일 뿐이며,
// 장비·펌웨어에 따라 PATCH가 거부될 수 있으므로, 이 단계가 실패해도 설치를
// 계속하는 동작을 유지합니다.
func TestBootContinuesWhenBootOrderPatchRejected(t *testing.T) {
	fake := newFakeIDRAC(t)
	fake.failBootOrderPatch = true
	idracServer := httptest.NewTLSServer(fake.handler())
	defer idracServer.Close()

	isoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer isoServer.Close()

	client := redfish.New(strings.TrimPrefix(idracServer.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := client.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer client.Logout()

	err := Boot(ctx, client, bmc.BootRequest{
		ISOURL:      isoServer.URL + "/worker1.iso",
		Waits:       bmc.Waits{},
		HTTPTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("BootOrder PATCH 거부는 설치를 중단시키면 안 됩니다(fail-open): %v", err)
	}

	// 보호 단계는 실패했지만 나머지 흐름은 끝까지 진행되어야 합니다.
	if len(fake.settings) != 0 {
		t.Errorf("PATCH가 거부됐으므로 부트 순서는 그대로여야 합니다: %v", fake.settings)
	}
	if !fake.media["1"].inserted {
		t.Error("ISO가 마운트되어야 합니다")
	}
	if fake.attributes["ServerBoot.1.FirstBootDevice"] != "VCD-DVD" {
		t.Errorf("원타임 부트 설정은 여전히 적용되어야 합니다: %v",
			fake.attributes["ServerBoot.1.FirstBootDevice"])
	}
	if len(fake.resets) != 2 || fake.resets[1] != "On" {
		t.Errorf("전원 ON까지 진행되어야 합니다: %v", fake.resets)
	}
}

// bootOrderClient는 removeVirtualMediaFromBootOrder 단위 테스트용으로
// 테스트 서버에 로그인한 클라이언트를 만듭니다.
func bootOrderClient(t *testing.T, fake *fakeIDRAC) *redfish.Client {
	t.Helper()
	server := httptest.NewTLSServer(fake.handler())
	t.Cleanup(server.Close)
	client := redfish.New(strings.TrimPrefix(server.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	if err := client.Login(context.Background(), "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	t.Cleanup(client.Logout)
	return client
}

// 부트 순서 보호는 fail-open입니다: 최초 System 조회가 실패해도
// 오류 대신 경고 후 통과해야 설치 작업(ISO 마운트, 원타임 부팅)이
// 계속됩니다.
func TestBootOrderProtectionContinuesWhenSystemGetFails(t *testing.T) {
	fake := newFakeIDRAC(t)
	fake.failNthSystemGet = 1
	client := bootOrderClient(t, fake)

	if err := removeVirtualMediaFromBootOrder(context.Background(), client); err != nil {
		t.Fatalf("System 조회 실패는 경고 후 통과여야 합니다: %v", err)
	}
	if fake.settings != nil {
		t.Errorf("조회 실패 시 부트 순서를 바꾸면 안 됩니다: %v", fake.settings)
	}
}

// 개별 부트 항목 조회가 실패하면 그 항목은 보수적으로 유지하고,
// 나머지 항목으로 보호를 계속해야 합니다.
func TestBootOrderProtectionKeepsUnreadableEntry(t *testing.T) {
	fake := newFakeIDRAC(t)
	// Boot0001(일반 디스크) 조회를 실패시키고 Boot0002(가상 미디어)만 남깁니다.
	fake.failBootOptionGets = map[string]bool{"Boot0001": true}
	client := bootOrderClient(t, fake)

	if err := removeVirtualMediaFromBootOrder(context.Background(), client); err != nil {
		t.Fatalf("항목 조회 실패는 유지 후 계속이어야 합니다: %v", err)
	}
	// 조회하지 못한 Boot0001은 유지되고, 가상 미디어 Boot0002만 빠져야 합니다.
	if len(fake.settings) != 1 || fake.settings[0] != "Boot0001" {
		t.Errorf("조회 실패 항목은 유지되어야 합니다: %v", fake.settings)
	}
}

// fail-open은 컨텍스트 취소(Ctrl+C)까지 삼키면 안 됩니다.
func TestBootOrderProtectionReturnsContextCancel(t *testing.T) {
	fake := newFakeIDRAC(t)
	client := bootOrderClient(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeVirtualMediaFromBootOrder(ctx, client); err == nil {
		t.Fatal("취소된 컨텍스트는 오류로 반환되어야 합니다")
	}
}

// 전체 흐름 회귀: 부트 순서 단계의 System 조회가 실패해도 boot는
// ISO 마운트와 원타임 부팅 설정까지 계속 진행해야 합니다.
func TestBootContinuesWhenBootOrderSystemGetFails(t *testing.T) {
	fake := newFakeIDRAC(t)
	// Waits{}에서는 전원 폴링의 제한 시간이 0이라 조회 요청이 서버에
	// 도달하지 않으므로, 서버 기준 첫 System GET이 부트 순서 단계입니다.
	fake.failNthSystemGet = 1

	if err := bootThrough(t, fake, bmc.Waits{}); err != nil {
		t.Fatalf("부트 순서 조회 실패에도 boot는 계속되어야 합니다: %v", err)
	}
	if !fake.media["1"].inserted {
		t.Error("ISO 마운트까지 진행되어야 합니다")
	}
	if fake.attributes["ServerBoot.1.FirstBootDevice"] != "VCD-DVD" {
		t.Error("원타임 부팅 설정까지 진행되어야 합니다")
	}
	if len(fake.resets) != 2 || fake.resets[1] != "On" {
		t.Errorf("전원 ON까지 진행되어야 합니다: %v", fake.resets)
	}
	if fake.settings != nil {
		t.Errorf("조회 실패 시 부트 순서를 바꾸면 안 됩니다: %v", fake.settings)
	}
}
