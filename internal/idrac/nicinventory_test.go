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

// fakeNICTree는 NIC 인벤토리 수집이 탐색하는 Redfish 트리의 최소 구현입니다.
// 실기에서 확인된 형태를 재현합니다: 2포트 카드가 장치 기능(NDF)은 두 개
// 노출하면서 NetworkPorts 컬렉션에는 포트 1만 만들어, 포트 2의
// PhysicalPortAssignment 링크가 404가 됩니다. port2Status로 포트 2 조회의
// 응답 상태를 바꿔 404 이외의 실패도 흉내 낼 수 있습니다.
func fakeNICTree(t *testing.T, port2Status int) http.Handler {
	t.Helper()
	const (
		chassis  = "/redfish/v1/Chassis/System.Embedded.1"
		adapters = chassis + "/NetworkAdapters"
		adapter  = adapters + "/NIC.Slot.1"
		ndfs     = adapter + "/NetworkDeviceFunctions"
		ports    = adapter + "/NetworkPorts"
	)
	ref := func(id string) map[string]string { return map[string]string{"@odata.id": id} }
	collection := func(ids ...string) map[string]any {
		members := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			members = append(members, ref(id))
		}
		return map[string]any{"Members@odata.count": len(ids), "Members": members}
	}

	routes := map[string]any{
		"/redfish/v1": map[string]any{
			"Systems": ref("/redfish/v1/Systems"), "Chassis": ref("/redfish/v1/Chassis"),
			"Oem": map[string]any{"Dell": map[string]any{"ServiceTag": "TEST123"}},
		},
		"/redfish/v1/Systems": collection(SystemPath),
		SystemPath: map[string]any{
			"Id": "System.Embedded.1", "Model": "PowerEdge R670",
			"EthernetInterfaces": ref(SystemPath + "/EthernetInterfaces"),
		},
		SystemPath + "/EthernetInterfaces": collection(SystemPath + "/EthernetInterfaces/NIC.Slot.1-2-1"),
		SystemPath + "/EthernetInterfaces/NIC.Slot.1-2-1": map[string]any{
			"Id": "NIC.Slot.1-2-1", "MACAddress": "aa:bb:cc:00:00:02",
			"LinkStatus": "LinkDown", "SpeedMbps": 10000,
		},
		"/redfish/v1/Chassis": collection(chassis),
		chassis:               map[string]any{"NetworkAdapters": ref(adapters)},
		adapters:              collection(adapter),
		adapter: map[string]any{
			"Id": "NIC.Slot.1", "Model": "Example 25G 2P",
			"NetworkDeviceFunctions": ref(ndfs),
		},
		ndfs: collection(ndfs+"/NIC.Slot.1-1-1", ndfs+"/NIC.Slot.1-2-1"),
		ndfs + "/NIC.Slot.1-1-1": map[string]any{
			"Id":       "NIC.Slot.1-1-1",
			"Ethernet": map[string]any{"PermanentMACAddress": "aa:bb:cc:00:00:01"},
			"Links":    map[string]any{"PhysicalPortAssignment": ref(ports + "/NIC.Slot.1-1")},
		},
		// 포트 2의 장치 기능은 존재하지만, 링크가 가리키는 포트 리소스는
		// NetworkPorts 컬렉션에 없습니다(아래에서 404).
		ndfs + "/NIC.Slot.1-2-1": map[string]any{
			"Id":       "NIC.Slot.1-2-1",
			"Ethernet": map[string]any{"PermanentMACAddress": "aa:bb:cc:00:00:02"},
			"Links":    map[string]any{"PhysicalPortAssignment": ref(ports + "/NIC.Slot.1-2")},
		},
		// 실제 장비처럼 NetworkPorts 컬렉션 자체는 존재하며 포트 1만
		// 담고 있습니다. 포트 2 리소스는 아래 핸들러에서 port2Status로
		// 응답합니다.
		ports: collection(ports + "/NIC.Slot.1-1"),
		ports + "/NIC.Slot.1-1": map[string]any{
			"Id": "NIC.Slot.1-1", "LinkStatus": "Up", "CurrentLinkSpeedMbps": 25000,
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == ports+"/NIC.Slot.1-2" {
			w.WriteHeader(port2Status)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/SessionService/Sessions" {
			w.Header().Set("X-Auth-Token", "tok")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/1")
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		payload, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func nicTreeClient(t *testing.T, port2Status int) *redfish.Client {
	t.Helper()
	server := httptest.NewTLSServer(fakeNICTree(t, port2Status))
	t.Cleanup(server.Close)
	client := redfish.New(strings.TrimPrefix(server.URL, "https://"),
		redfish.Options{Timeout: 5 * time.Second})
	if err := client.Login(context.Background(), "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	t.Cleanup(client.Logout)
	return client
}

// 실기 회귀 테스트: BMC가 광고한 포트 링크가 404여도 수집은 중단되지 않고,
// 해당 포트는 장치 기능과 EthernetInterfaces 값으로 채워져야 합니다.
func TestCollectNICInventoryContinuesOnDanglingPortLink(t *testing.T) {
	client := nicTreeClient(t, http.StatusNotFound)

	inv, err := CollectNICInventory(context.Background(), client)
	if err != nil {
		t.Fatalf("끊긴 포트 링크에도 수집은 계속되어야 합니다: %v", err)
	}
	if len(inv.Adapters) != 1 || len(inv.Adapters[0].Ports) != 2 {
		t.Fatalf("어댑터 1개, 포트 2개가 수집되어야 합니다: %+v", inv.Adapters)
	}

	p1, p2 := inv.Adapters[0].Ports[0], inv.Adapters[0].Ports[1]
	if p1.ID != "NIC.Slot.1-1" || p1.CurrentSpeedMbps == nil || *p1.CurrentSpeedMbps != 25000 {
		t.Errorf("포트 1은 포트 리소스 값으로 채워져야 합니다: %+v", p1)
	}
	if p2.ID != "NIC.Slot.1-2" {
		t.Errorf("포트 2의 ID는 장치 기능에서 유도되어야 합니다: %+v", p2)
	}
	if p2.PermanentMACAddress != "AA:BB:CC:00:00:02" {
		t.Errorf("포트 2의 영구 MAC은 장치 기능 값이어야 합니다: %+v", p2)
	}
	// 링크 상태와 속도는 EthernetInterfaces 보충 값에서 와야 합니다.
	if p2.LinkStatus != "Down" {
		t.Errorf("포트 2의 링크 상태는 EthernetInterfaces 폴백(정규화된 Down)이어야 합니다: %+v", p2)
	}
	if p2.CurrentSpeedMbps == nil || *p2.CurrentSpeedMbps != 10000 {
		t.Errorf("포트 2의 속도는 EthernetInterfaces 폴백이어야 합니다: %+v", p2)
	}
}

// fail-open은 404(광고된 리소스 없음)에만 적용됩니다. 서버 오류 등
// 다른 실패는 장비 특성이 아니라 실제 문제이므로 수집을 중단해야 합니다.
func TestCollectNICInventoryFailsOnPortServerError(t *testing.T) {
	client := nicTreeClient(t, http.StatusInternalServerError)

	if _, err := CollectNICInventory(context.Background(), client); err == nil {
		t.Fatal("포트 조회의 서버 오류(HTTP 500)는 수집을 중단해야 합니다")
	}
}
