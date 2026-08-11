// Package redfish는 BMC의 Redfish API 클라이언트를 제공합니다.
// 세션과 출처 검증 등 프로토콜 계층만 다루므로 장비 종류와 무관하게 공유합니다.
//
//   - 세션을 한 번 만들고 X-Auth-Token을 모든 요청에 재사용합니다.
//   - 정상 종료, 오류, 취소 어느 경우에도 세션을 DELETE로 정리합니다.
//   - 토큰과 비밀번호는 로그에 남기지 않습니다.
//   - 프록시 환경 변수를 사용하지 않고 BMC에 직접 연결합니다.
//     사설 관리망 접근과 인증 정보의 전달 경로를 명확하게 유지하기 위해서입니다.
package redfish

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"upi-forge/internal/logx"
)

// Options는 클라이언트 동작을 조정합니다.
type Options struct {
	// TLSVerify가 false면 BMC의 자체 서명 인증서를 허용합니다.
	TLSVerify bool
	// Timeout은 HTTP 요청 제한 시간입니다.
	Timeout time.Duration
}

// Client는 BMC 한 대에 대한 Redfish 세션을 관리합니다.
type Client struct {
	host string // 설정에서 받은 원본 주소 (호스트 또는 호스트:포트)
	// origin은 이 클라이언트가 토큰을 전송할 수 있는 유일한 대상입니다.
	// BMC 응답의 주소를 검증 없이 따라가지 않도록 보관합니다.
	originHost string
	originPort string

	http *http.Client

	token      string
	sessionURI string
}

// ErrForeignOrigin은 Redfish 응답이 다른 호스트를 가리킬 때 반환합니다.
var ErrForeignOrigin = errors.New("Redfish 응답이 다른 호스트를 가리킵니다")

// APIError는 Redfish가 2xx 이외의 응답을 돌려줬을 때의 오류입니다.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("Redfish %s 실패 (HTTP %d): %s", e.Method, e.StatusCode, e.URL)
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		msg += "\n확인: BMC 계정/비밀번호, 계정 잠금 및 Login 권한"
	case http.StatusNotFound:
		msg += "\n확인: 응답의 @odata.id와 BMC 펌웨어/리소스 지원 여부"
	case http.StatusTooManyRequests:
		msg += "\n확인: 세션 수 제한. 사용하지 않는 활성 세션을 정리하세요."
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		msg += "\n확인: BMC 상태(iDRAC은 Lifecycle Controller 포함)와 ExtendedInfo 메시지"
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		msg += "\nBMC 응답: " + truncate(body, 2000)
	}
	return msg
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + " ...(생략)"
}

// New는 BMC 주소에 대한 클라이언트를 만듭니다. 아직 로그인하지는 않습니다.
func New(host string, o Options) *Client {
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	transport := &http.Transport{
		// 관리망의 BMC에 직접 연결합니다.
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !o.TLSVerify, //nolint:gosec // BMC 자체 서명 인증서 허용
			MinVersion:         tls.VersionTLS12,
		},
		TLSHandshakeTimeout: 20 * time.Second,
	}

	// host는 idracs.csv에서 온 IP이거나 "호스트:포트" 형태일 수 있습니다.
	originHost, originPort := splitHostPort(host)

	c := &Client{
		host:       host,
		originHost: originHost,
		originPort: originPort,
	}
	c.http = &http.Client{
		Timeout:   o.Timeout,
		Transport: transport,
		// X-Auth-Token이 외부 호스트로 전달되지 않도록 같은 출처의
		// 리다이렉트만 따라갑니다.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("리다이렉트가 너무 많습니다: %s", req.URL.Redacted())
			}
			if err := c.checkOrigin(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
	return c
}

// splitHostPort는 "호스트" 또는 "호스트:포트"를 나눕니다. 포트가 없으면 443입니다.
func splitHostPort(host string) (string, string) {
	if h, p, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(h), p
	}
	return strings.ToLower(strings.Trim(host, "[]")), "443"
}

// checkOrigin은 URL이 이 클라이언트가 붙은 BMC와 같은 대상인지 확인합니다.
func (c *Client) checkOrigin(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("%w: https가 아닙니다: %s", ErrForeignOrigin, u.Redacted())
	}
	if u.User != nil {
		return fmt.Errorf("%w: URL에 인증 정보가 들어 있습니다: %s", ErrForeignOrigin, u.Redacted())
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if host != c.originHost || port != c.originPort {
		return fmt.Errorf("%w: %s (허용: %s)", ErrForeignOrigin, u.Redacted(),
			net.JoinHostPort(c.originHost, c.originPort))
	}
	return nil
}

// Host는 클라이언트가 붙은 BMC 주소입니다.
func (c *Client) Host() string { return c.host }

// Root는 Redfish 서비스 루트 URL입니다.
func (c *Client) Root() string { return "https://" + c.host + "/redfish/v1" }

// Resolve는 Redfish 응답에 들어 있는 참조(@odata.id, Action target)를
// 실제 요청 URL로 바꿉니다.
//
// 상대 경로는 현재 BMC 주소를 기준으로 해석합니다.
// 절대 URL이 오면 같은 대상인지 검증하고, 다르면 거부합니다.
// 손상되었거나 악의적인 BMC가 외부 호스트를 가리키는 참조를 돌려주면
// 세션 토큰이 그 호스트로 나갈 수 있기 때문입니다.
func (c *Client) Resolve(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("Redfish 참조가 비어 있습니다")
	}
	if !strings.Contains(ref, "://") {
		if !strings.HasPrefix(ref, "/") {
			ref = "/" + ref
		}
		return "https://" + c.host + ref, nil
	}

	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("Redfish 참조를 해석하지 못했습니다: %q: %w", ref, err)
	}
	if err := c.checkOrigin(u); err != nil {
		return "", err
	}
	return u.String(), nil
}

// Login은 Redfish 세션을 만들고 X-Auth-Token을 보관합니다.
func (c *Client) Login(ctx context.Context, username, password string) error {
	body, err := json.Marshal(map[string]string{"UserName": username, "Password": password})
	if err != nil {
		return fmt.Errorf("세션 요청 본문을 만들지 못했습니다: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Root()+"/SessionService/Sessions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("세션 생성 요청을 만들지 못했습니다: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("BMC 세션 생성 요청을 전송하지 못했습니다: %s\n"+
			"확인: BMC 주소, TCP 443, 방화벽, 라우팅 및 BMC HTTPS 서비스: %w", c.host, err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return &APIError{Method: "세션 생성", URL: req.URL.String(),
			StatusCode: resp.StatusCode, Body: string(payload)}
	}

	// 헤더 이름은 대소문자를 구분하지 않습니다. http.Header.Get이 알아서 처리합니다.
	token := resp.Header.Get("X-Auth-Token")
	if token == "" {
		return fmt.Errorf("HTTP 201 응답에서 X-Auth-Token 헤더를 찾지 못했습니다: %s", c.host)
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return fmt.Errorf("HTTP 201 응답에서 세션 Location 헤더를 찾지 못했습니다: %s\n"+
			"자동 삭제할 주소가 없으므로 BMC 활성 세션을 직접 확인하세요", c.host)
	}
	if u, err := url.Parse(location); err == nil && u.Path != "" {
		location = u.Path
	}

	c.token = token
	c.sessionURI = location
	logx.Debug("%s: Redfish 세션 생성 완료 (%s)", c.host, location)
	return nil
}

// Logout은 세션을 삭제합니다. defer로 호출하는 것을 전제로 하며,
// 실패해도 상위 작업 결과를 덮어쓰지 않도록 오류를 경고로만 남깁니다.
func (c *Client) Logout() {
	if c.token == "" || c.sessionURI == "" {
		return
	}
	// 상위 컨텍스트가 이미 취소된 상태에서도 세션은 반드시 정리해야 합니다.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionURL, resolveErr := c.Resolve(c.sessionURI)
	if resolveErr != nil {
		logx.Warn("%s: 세션 주소가 올바르지 않아 삭제하지 못했습니다: %v", c.host, resolveErr)
		c.token, c.sessionURI = "", ""
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sessionURL, nil)
	if err == nil {
		req.Header.Set("X-Auth-Token", c.token)
		resp, err := c.http.Do(req)
		if err == nil {
			defer drainClose(resp)
			switch resp.StatusCode {
			case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
				logx.Debug("%s: Redfish 세션 삭제 완료 (HTTP %d)", c.host, resp.StatusCode)
			default:
				logx.Warn("%s: 세션 삭제가 확인되지 않았습니다 (HTTP %d). "+
					"BMC SessionService에서 활성 세션을 확인하세요.", c.host, resp.StatusCode)
			}
			c.token, c.sessionURI = "", ""
			return
		}
	}
	logx.Warn("%s: BMC 세션을 삭제하지 못했습니다. SessionService에서 활성 세션을 확인하세요.", c.host)
	c.token, c.sessionURI = "", ""
}

func (c *Client) do(ctx context.Context, method, rawURL string, payload any) ([]byte, error) {
	// 토큰을 붙이기 직전에 대상을 한 번 더 확인합니다.
	// 호출자가 Resolve를 거치므로 보통 통과하지만, 이 검사가 마지막 방어선입니다.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("요청 주소를 해석하지 못했습니다: %q: %w", rawURL, err)
	}
	if err := c.checkOrigin(parsed); err != nil {
		return nil, err
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("요청 본문을 만들지 못했습니다: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("요청을 만들지 못했습니다: %s: %w", rawURL, err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-Auth-Token", c.token)
	}

	logx.Debug("%s %s", method, rawURL)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("BMC Redfish 연결에 실패했습니다: %s\n"+
			"확인: BMC 주소, TCP 443, 방화벽, 라우팅 및 TLS 연결: %w", rawURL, err)
	}
	defer drainClose(resp)

	// Redfish 응답은 수 KB~수백 KB입니다. 손상되었거나 악의적인 BMC가
	// 거대한 본문을 보내 bastion 메모리를 고갈시키지 못하도록 상한을 둡니다.
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Method: method, URL: rawURL,
			StatusCode: resp.StatusCode, Body: string(data)}
	}
	if readErr != nil {
		return nil, fmt.Errorf("응답 본문을 읽지 못했습니다: %s: %w", rawURL, readErr)
	}
	return data, nil
}

// Get은 리소스를 읽어 out에 디코딩합니다. out이 nil이면 본문을 버립니다.
func (c *Client) Get(ctx context.Context, ref string, out any) error {
	rawURL, err := c.Resolve(ref)
	if err != nil {
		return err
	}
	data, err := c.do(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("HTTP 200이지만 응답 본문이 비어 있습니다: %s\n"+
			"Lifecycle Controller 인벤토리 작업이 끝났는지 확인하세요", rawURL)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("JSON 형식이 아닌 응답을 받았습니다: %s\n"+
			"프록시 또는 로그인 페이지가 대신 반환되지 않았는지 확인하세요: %w", rawURL, err)
	}
	return nil
}

// Post는 Redfish 액션을 호출합니다.
func (c *Client) Post(ctx context.Context, ref string, payload any) ([]byte, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	rawURL, err := c.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, rawURL, payload)
}

// Patch는 리소스 속성을 변경합니다.
func (c *Client) Patch(ctx context.Context, ref string, payload any) ([]byte, error) {
	rawURL, err := c.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPatch, rawURL, payload)
}

// maxResponseBytes는 Redfish 응답 본문의 상한입니다. 실제 Redfish 응답은
// 수 KB~수백 KB이므로 8MB면 충분하고, 손상/악성 BMC의 거대 응답으로부터
// bastion 메모리를 보호합니다.
const maxResponseBytes = 8 << 20

func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// Collection은 Redfish 컬렉션 응답의 공통 형태입니다.
type Collection struct {
	Members      []Ref `json:"Members"`
	MembersCount *int  `json:"Members@odata.count"`
}

// Ref는 @odata.id 링크입니다.
type Ref struct {
	ID string `json:"@odata.id"`
}

// IDs는 컬렉션 구성원 링크 목록을 반환합니다.
func (c Collection) IDs() []string {
	out := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

// Check는 선언한 개수와 실제 Members 길이가 같은지 확인합니다.
func (c Collection) Check(name string) error {
	if c.MembersCount == nil {
		return nil
	}
	if *c.MembersCount != len(c.Members) {
		return fmt.Errorf("%s count(%d)와 Members 배열 길이(%d)가 다릅니다",
			name, *c.MembersCount, len(c.Members))
	}
	return nil
}

// Status는 Redfish 리소스의 상태 블록입니다.
type Status struct {
	Health string `json:"Health"`
	State  string `json:"State"`
}
