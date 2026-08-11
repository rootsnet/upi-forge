package idrac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"upi-forge/internal/redfish"
)

func TestPCIAddressFromFunctionID(t *testing.T) {
	cases := map[string]string{
		"0-60-0-0":  "0000:3c:00.0",
		"0-174-0-0": "0000:ae:00.0",
		"0-8-0-1":   "0000:08:00.1",
		"0-8-0":     "", // 형식이 다르면 빈 문자열
		"a-b-c-d":   "",
	}
	for id, want := range cases {
		if got := pciAddressFromFunctionID(id); got != want {
			t.Errorf("pciAddressFromFunctionID(%q): got=%q want=%q", id, got, want)
		}
	}
}

func TestNormalizeLinkStatus(t *testing.T) {
	cases := map[string]string{
		"Up": "Up", "LinkUp": "Up", "Connected": "Up",
		"Down": "Down", "LinkDown": "Down", "Disconnected": "Down",
		"": "Unknown", "Weird": "Weird",
	}
	for in, want := range cases {
		if got := normalizeLinkStatus(in); got != want {
			t.Errorf("normalizeLinkStatus(%q): got=%q want=%q", in, got, want)
		}
	}
}

func TestNaturalLessSortsSlotNumbers(t *testing.T) {
	// 사전순이면 NIC.Slot.10이 NIC.Slot.2보다 앞에 오므로 잘못됩니다.
	if !naturalLess("NIC.Slot.2", "NIC.Slot.10") {
		t.Error("NIC.Slot.2가 NIC.Slot.10보다 앞이어야 합니다")
	}
	if naturalLess("NIC.Slot.10", "NIC.Slot.2") {
		t.Error("역방향 비교가 잘못되었습니다")
	}
	if !naturalLess("NIC.Slot.5-1-1", "NIC.Slot.5-2-1") {
		t.Error("포트 번호 비교가 잘못되었습니다")
	}
}

func TestIsVirtualMediaBootOption(t *testing.T) {
	byName := BootOption{DisplayName: "Virtual Optical Drive 1.1"}
	if !isVirtualMediaBootOption(byName) {
		t.Error("DisplayName으로 가상 미디어를 판별해야 합니다")
	}
	byLink := BootOption{
		DisplayName: "Unknown",
		RelatedItem: []redfish.Ref{{ID: "/redfish/v1/Systems/System.Embedded.1/VirtualMedia/1"}},
	}
	if !isVirtualMediaBootOption(byLink) {
		t.Error("RelatedItem 링크로 가상 미디어를 판별해야 합니다")
	}
	normal := BootOption{
		DisplayName: "PCIe SSD in Slot 1",
		RelatedItem: []redfish.Ref{{ID: "/redfish/v1/Systems/System.Embedded.1/Storage/Drives/1"}},
	}
	if isVirtualMediaBootOption(normal) {
		t.Error("일반 부트 항목을 가상 미디어로 판별했습니다")
	}
}

func TestVirtualMediaSupportsOpticalInsert(t *testing.T) {
	var usable VirtualMedia
	usable.MediaTypes = []string{"CD", "DVD"}
	usable.Actions.Insert.Target = "/redfish/v1/.../VirtualMedia.InsertMedia"
	if !usable.SupportsOpticalInsert() {
		t.Error("CD/DVD와 InsertMedia가 있으면 사용 가능해야 합니다")
	}

	var noAction VirtualMedia
	noAction.MediaTypes = []string{"CD"}
	if noAction.SupportsOpticalInsert() {
		t.Error("InsertMedia 액션이 없으면 사용할 수 없습니다")
	}

	var usbOnly VirtualMedia
	usbOnly.MediaTypes = []string{"USBStick"}
	usbOnly.Actions.Insert.Target = "target"
	if usbOnly.SupportsOpticalInsert() {
		t.Error("CD/DVD를 지원하지 않으면 사용할 수 없습니다")
	}
}

func TestCheckISOURLAcceptsRangeResponses(t *testing.T) {
	full := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer full.Close()
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer partial.Close()
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer missing.Close()

	ctx := context.Background()
	if err := CheckISOURL(ctx, full.URL+"/x.iso", 5*time.Second); err != nil {
		t.Errorf("HTTP 200은 성공이어야 합니다: %v", err)
	}
	if err := CheckISOURL(ctx, partial.URL+"/x.iso", 5*time.Second); err != nil {
		t.Errorf("HTTP 206은 성공이어야 합니다: %v", err)
	}
	if err := CheckISOURL(ctx, missing.URL+"/x.iso", 5*time.Second); err == nil {
		t.Error("HTTP 404는 실패여야 합니다")
	}
}

// TestSessionLifecycle은 세션 생성과 정리가 실제 HTTP 흐름에서 동작하는지 확인합니다.
func TestSessionLifecycle(t *testing.T) {
	var deleted bool
	var authHeaders []string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/SessionService/Sessions":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["UserName"] != "root" || body["Password"] != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-Auth-Token", "token-123")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/1")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete:
			deleted = true
			authHeaders = append(authHeaders, r.Header.Get("X-Auth-Token"))
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == SystemPath:
			authHeaders = append(authHeaders, r.Header.Get("X-Auth-Token"))
			_ = json.NewEncoder(w).Encode(System{ID: "System.Embedded.1", PowerState: "On", Model: "PowerEdge R670"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "https://")
	client := redfish.New(host, redfish.Options{Timeout: 5 * time.Second})

	ctx := context.Background()
	if err := client.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	sys, err := GetSystem(ctx, client)
	if err != nil {
		t.Fatalf("GetSystem 실패: %v", err)
	}
	if sys.Model != "PowerEdge R670" || sys.PowerState != "On" {
		t.Errorf("System 값이 다릅니다: %+v", sys)
	}
	client.Logout()

	if !deleted {
		t.Error("세션이 삭제되지 않았습니다")
	}
	for _, header := range authHeaders {
		if header != "token-123" {
			t.Errorf("모든 요청이 같은 토큰을 재사용해야 합니다: %q", header)
		}
	}
}

func TestLoginFailureReturnsAPIError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad credentials"}}`))
	}))
	defer server.Close()

	client := redfish.New(strings.TrimPrefix(server.URL, "https://"), redfish.Options{Timeout: 5 * time.Second})
	err := client.Login(context.Background(), "root", "wrong")
	if err == nil {
		t.Fatal("잘못된 인증은 오류여야 합니다")
	}
	if !strings.Contains(err.Error(), "BMC 계정") {
		t.Errorf("401에 대한 안내가 있어야 합니다: %v", err)
	}
}
