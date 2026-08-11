package redfish_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"upi-forge/internal/redfish"
)

func hostOf(server *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(server.URL, "https://"), "http://")
}

func TestResolveAcceptsRelativeReference(t *testing.T) {
	c := redfish.New("198.51.100.110", redfish.Options{})

	cases := map[string]string{
		"/redfish/v1/Systems": "https://198.51.100.110/redfish/v1/Systems",
		"redfish/v1/Systems":  "https://198.51.100.110/redfish/v1/Systems",
	}
	for ref, want := range cases {
		got, err := c.Resolve(ref)
		if err != nil {
			t.Errorf("Resolve(%q) 실패: %v", ref, err)
			continue
		}
		if got != want {
			t.Errorf("Resolve(%q): got=%q want=%q", ref, got, want)
		}
	}
	if _, err := c.Resolve(""); err == nil {
		t.Error("빈 참조는 오류여야 합니다")
	}
}

func TestResolveAcceptsSameOriginAbsoluteURL(t *testing.T) {
	c := redfish.New("198.51.100.110", redfish.Options{})

	// 포트를 생략한 https는 443과 같은 대상으로 봅니다.
	for _, ref := range []string{
		"https://198.51.100.110/redfish/v1/Systems",
		"https://198.51.100.110:443/redfish/v1/Systems",
		"https://198.51.100.110/redfish/v1/Systems/System.Embedded.1/VirtualMedia/1",
	} {
		if _, err := c.Resolve(ref); err != nil {
			t.Errorf("같은 대상 절대 URL은 허용해야 합니다: %q: %v", ref, err)
		}
	}

	// 포트를 명시한 클라이언트는 그 포트만 허용합니다.
	withPort := redfish.New("198.51.100.110:8443", redfish.Options{})
	if _, err := withPort.Resolve("https://198.51.100.110:8443/redfish/v1"); err != nil {
		t.Errorf("같은 포트는 허용해야 합니다: %v", err)
	}
	if _, err := withPort.Resolve("https://198.51.100.110/redfish/v1"); !errors.Is(err, redfish.ErrForeignOrigin) {
		t.Errorf("포트가 다르면 거부해야 합니다: %v", err)
	}
}

// TestResolveRejectsForeignOrigin은 손상되었거나 악의적인 BMC가
// 외부 호스트를 가리키는 참조를 돌려줬을 때 세션 토큰이 그쪽으로
// 나가지 않는지 확인합니다.
func TestResolveRejectsForeignOrigin(t *testing.T) {
	c := redfish.New("198.51.100.110", redfish.Options{})

	cases := map[string]string{
		"다른 호스트":       "https://evil.example.com/redfish/v1/Systems",
		"다른 포트":        "https://198.51.100.110:9443/redfish/v1",
		"http 평문":      "http://198.51.100.110/redfish/v1",
		"userinfo 포함":  "https://attacker@198.51.100.110/redfish/v1",
		"호스트 뒤에 붙인 형태": "https://198.51.100.110.evil.example.com/redfish/v1",
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Resolve(ref); !errors.Is(err, redfish.ErrForeignOrigin) {
				t.Errorf("거부해야 합니다: %q: %v", ref, err)
			}
		})
	}
}

// TestGetRejectsForeignOdataID는 컬렉션 응답에 외부 호스트 링크가 섞여도
// 그 주소로 요청을 보내지 않는지 확인합니다.
func TestGetRejectsForeignOdataID(t *testing.T) {
	var leaked bool
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != "" {
			leaked = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()

	idrac := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("X-Auth-Token", "tok")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/1")
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer idrac.Close()

	c := redfish.New(hostOf(idrac), redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := c.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer c.Logout()

	// BMC가 외부 호스트를 가리키는 @odata.id를 돌려준 상황입니다.
	err := c.Get(ctx, foreign.URL+"/redfish/v1/Systems", nil)
	if !errors.Is(err, redfish.ErrForeignOrigin) {
		t.Fatalf("외부 호스트 참조는 거부해야 합니다: %v", err)
	}
	if leaked {
		t.Error("외부 호스트로 X-Auth-Token이 전송되었습니다")
	}
}

// TestRedirectToForeignOriginIsRejected는 BMC가 외부 호스트로
// 리다이렉트해도 토큰이 따라가지 않는지 확인합니다.
func TestRedirectToForeignOriginIsRejected(t *testing.T) {
	var leaked bool
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != "" {
			leaked = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer foreign.Close()

	idrac := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("X-Auth-Token", "tok")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/1")
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/Systems"):
			http.Redirect(w, r, foreign.URL+"/redfish/v1/Systems", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer idrac.Close()

	c := redfish.New(hostOf(idrac), redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := c.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer c.Logout()

	var out map[string]any
	err := c.Get(ctx, "/redfish/v1/Systems", &out)
	if err == nil {
		t.Fatal("외부 호스트로의 리다이렉트는 실패해야 합니다")
	}
	if !strings.Contains(err.Error(), "다른 호스트") {
		t.Errorf("origin 오류여야 합니다: %v", err)
	}
	if leaked {
		t.Error("리다이렉트를 따라가며 X-Auth-Token이 외부로 전송되었습니다")
	}
}

// TestRedirectWithinSameOriginIsFollowed는 같은 BMC 안의 리다이렉트는
// 정상적으로 따라가는지 확인합니다.
func TestRedirectWithinSameOriginIsFollowed(t *testing.T) {
	idrac := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("X-Auth-Token", "tok")
			w.Header().Set("Location", "/redfish/v1/SessionService/Sessions/1")
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/redfish/v1/Systems":
			http.Redirect(w, r, "/redfish/v1/Systems/", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"Id":"ok"}`))
		}
	}))
	defer idrac.Close()

	c := redfish.New(hostOf(idrac), redfish.Options{Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := c.Login(ctx, "root", "secret"); err != nil {
		t.Fatalf("Login 실패: %v", err)
	}
	defer c.Logout()

	var out struct {
		ID string `json:"Id"`
	}
	if err := c.Get(ctx, "/redfish/v1/Systems", &out); err != nil {
		t.Fatalf("같은 대상 리다이렉트는 따라가야 합니다: %v", err)
	}
	if out.ID != "ok" {
		t.Errorf("응답을 받지 못했습니다: %+v", out)
	}
}
