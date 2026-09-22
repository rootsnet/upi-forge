// Package config는 upi-forge의 공통 설정과 pathset 설정을 읽고 검증합니다.
// 설정값은 타입이 있는 구조체로 변환하며, 알 수 없는 키와 잘못된 형식은
// 작업을 시작하기 전에 오류로 반환합니다.
//
// 경로 규칙:
//   - 설정 파일 안의 상대 경로는 모두 "설정 파일이 있는 디렉터리" 기준입니다.
//   - 산출물이 쌓이는 작업 디렉터리(Workspace.Dir)만 현재 작업 디렉터리 기준이며,
//     비워 두면 현재 디렉터리를 사용합니다.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"upi-forge/internal/yamlx"
)

// DefaultPath는 --config를 생략했을 때 사용할 기본 설정 파일 경로입니다.
const DefaultPath = "configs/upi-forge.yaml"

// Config는 upi-forge.yaml의 전체 설정입니다.
type Config struct {
	Cluster   Cluster
	Registry  Registry
	WebServer WebServer
	Workspace Workspace
	Templates Templates
	Pathsets  Pathsets
	Logger    Logger
	Redfish   Redfish
	Tools     Tools

	path    string // 읽어 들인 설정 파일 경로
	baseDir string // 설정 파일이 있는 디렉터리
}

// Cluster는 대상 OpenShift 클러스터 정보입니다.
type Cluster struct {
	// Domain은 추가할 worker가 접속할 클러스터 도메인입니다. 예: mycluster.example.com
	Domain string
	// APIURL을 비우면 https://api.<domain>:6443 을 사용합니다.
	APIURL string
	// Version을 비우면 현재 oc 로그인 클러스터의 버전을 조회합니다.
	Version string
	// MCSPort는 Machine Config Server 포트입니다. 기본값 22623.
	MCSPort int
}

// Registry는 release 이미지를 가져올 레지스트리 정보입니다.
type Registry struct {
	// PullSecret은 release 이미지 접근에 사용할 pull secret 경로입니다.
	PullSecret string
	// Registry는 연결 환경의 quay.io 또는 폐쇄망(disconnected) 환경의
	// 미러 레지스트리입니다.
	// 예: quay.io, mirror.example.com/ocp4
	Registry string
}

// WebServer는 ISO와 rootfs를 게시하는 웹 서버 정보입니다.
type WebServer struct {
	URL  string // 예: http://192.0.2.50:8080
	Path string // 예: /iso
}

// Workspace는 산출물이 쌓이는 작업 디렉터리와 파일 이름입니다.
type Workspace struct {
	Dir         string // 비우면 현재 작업 디렉터리
	TmpDir      string // 이미지 추출용 임시 디렉터리 이름
	FullISO     string // coreos-x86_64.iso
	MinimalISO  string // coreos-x86_64-minimal.iso
	RootfsImage string // coreos-x86_64-rootfs.img
	Installer   string // coreos-installer
}

// Templates는 Ignition 템플릿 경로입니다.
type Templates struct {
	WorkerPointerIgnition string
}

// Pathsets는 하드웨어 그룹 선택 정보입니다.
type Pathsets struct {
	// Active는 설치 작업에 사용할 pathset 이름입니다.
	Active string
	// Dir는 pathset 디렉터리들이 모여 있는 상위 디렉터리입니다.
	Dir string
}

// Logger는 로그 출력 옵션입니다.
type Logger struct {
	Debug     bool
	Timestamp bool
}

// Redfish는 iDRAC Redfish 호출 옵션입니다.
type Redfish struct {
	// TLSVerify가 false면 iDRAC의 자체 서명 인증서를 허용합니다.
	TLSVerify bool
	// TimeoutSeconds는 HTTP 요청 제한 시간입니다.
	TimeoutSeconds int
	// 아래 값은 각 Redfish 작업 뒤에 기다릴 시간입니다.
	// PowerOffWaitSeconds는 전원 꺼짐 확인 폴링의 최대 대기이며,
	// Off가 확인되면 즉시 다음 단계로 진행합니다. 나머지는 고정 대기입니다.
	PowerOffWaitSeconds  int
	MediaSettleSeconds   int
	AttributeWaitSeconds int
	PowerOnWaitSeconds   int
}

// Tools는 upi-forge가 실행할 외부 명령의 경로입니다.
type Tools struct {
	OC     string // oc
	Butane string // butane
	// CoreOSInstaller를 비우면 작업 디렉터리의 Workspace.Installer를 사용합니다.
	CoreOSInstaller string
}

// Load는 설정 파일을 읽고 기본값을 채운 뒤 검증합니다.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("설정 파일 경로를 확인하지 못했습니다: %s: %w", path, err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("설정 파일을 읽지 못했습니다: %s: %w", abs, err)
	}
	root, err := yamlx.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("설정 파일을 해석하지 못했습니다: %s: %w", abs, err)
	}

	cfg := &Config{path: abs, baseDir: filepath.Dir(abs)}
	if err := cfg.decode(root); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// decode는 YAML 트리를 구조체에 옮깁니다.
// 어떤 설정 키가 어느 필드로 가는지 이 함수 하나만 보면 알 수 있습니다.
func (c *Config) decode(root *yamlx.Node) error {
	m := yamlx.NewMapper(root)

	cluster := m.Section("cluster")
	cluster.String("domain", &c.Cluster.Domain)
	cluster.String("apiURL", &c.Cluster.APIURL)
	cluster.String("version", &c.Cluster.Version)
	cluster.Int("mcsPort", &c.Cluster.MCSPort)

	registry := m.Section("registry")
	registry.String("pullSecret", &c.Registry.PullSecret)
	registry.String("registry", &c.Registry.Registry)

	web := m.Section("webServer")
	web.String("url", &c.WebServer.URL)
	web.String("path", &c.WebServer.Path)

	workspace := m.Section("workspace")
	workspace.String("dir", &c.Workspace.Dir)
	workspace.String("tmpDir", &c.Workspace.TmpDir)
	workspace.String("fullISO", &c.Workspace.FullISO)
	workspace.String("minimalISO", &c.Workspace.MinimalISO)
	workspace.String("rootfsImage", &c.Workspace.RootfsImage)
	workspace.String("installer", &c.Workspace.Installer)

	templates := m.Section("templates")
	templates.String("workerPointerIgnition", &c.Templates.WorkerPointerIgnition)

	pathsets := m.Section("pathsets")
	pathsets.String("active", &c.Pathsets.Active)
	pathsets.String("dir", &c.Pathsets.Dir)

	logger := m.Section("logger")
	logger.Bool("debug", &c.Logger.Debug)
	logger.Bool("timestamp", &c.Logger.Timestamp)

	redfish := m.Section("redfish")
	redfish.Bool("tlsVerify", &c.Redfish.TLSVerify)
	redfish.Int("timeoutSeconds", &c.Redfish.TimeoutSeconds)
	redfish.Int("powerOffWaitSeconds", &c.Redfish.PowerOffWaitSeconds)
	redfish.Int("mediaSettleSeconds", &c.Redfish.MediaSettleSeconds)
	redfish.Int("attributeWaitSeconds", &c.Redfish.AttributeWaitSeconds)
	redfish.Int("powerOnWaitSeconds", &c.Redfish.PowerOnWaitSeconds)

	tools := m.Section("tools")
	tools.String("oc", &c.Tools.OC)
	tools.String("butane", &c.Tools.Butane)
	tools.String("coreosInstaller", &c.Tools.CoreOSInstaller)

	return m.Finish()
}

func (c *Config) applyDefaults() {
	if c.Cluster.MCSPort == 0 {
		c.Cluster.MCSPort = 22623
	}
	if c.Cluster.APIURL == "" && c.Cluster.Domain != "" {
		c.Cluster.APIURL = fmt.Sprintf("https://api.%s:6443", c.Cluster.Domain)
	}
	c.Cluster.APIURL = strings.TrimRight(c.Cluster.APIURL, "/")

	if c.Registry.Registry == "" {
		c.Registry.Registry = "quay.io"
	}

	c.WebServer.URL = strings.TrimRight(c.WebServer.URL, "/")
	c.WebServer.Path = strings.Trim(c.WebServer.Path, "/")
	if c.WebServer.Path != "" {
		c.WebServer.Path = "/" + c.WebServer.Path
	}

	if c.Workspace.TmpDir == "" {
		c.Workspace.TmpDir = "tmp"
	}
	if c.Workspace.FullISO == "" {
		c.Workspace.FullISO = "coreos-x86_64.iso"
	}
	if c.Workspace.MinimalISO == "" {
		c.Workspace.MinimalISO = "coreos-x86_64-minimal.iso"
	}
	if c.Workspace.RootfsImage == "" {
		c.Workspace.RootfsImage = "coreos-x86_64-rootfs.img"
	}
	if c.Workspace.Installer == "" {
		c.Workspace.Installer = "coreos-installer"
	}

	if c.Templates.WorkerPointerIgnition == "" {
		c.Templates.WorkerPointerIgnition = "templates/worker-pointer.ign.template"
	}
	if c.Pathsets.Dir == "" {
		c.Pathsets.Dir = "pathsets"
	}

	if c.Redfish.TimeoutSeconds == 0 {
		c.Redfish.TimeoutSeconds = 60
	}
	if c.Redfish.PowerOffWaitSeconds == 0 {
		c.Redfish.PowerOffWaitSeconds = 10
	}
	if c.Redfish.MediaSettleSeconds == 0 {
		c.Redfish.MediaSettleSeconds = 5
	}
	if c.Redfish.AttributeWaitSeconds == 0 {
		c.Redfish.AttributeWaitSeconds = 5
	}
	if c.Redfish.PowerOnWaitSeconds == 0 {
		c.Redfish.PowerOnWaitSeconds = 10
	}

	if c.Tools.OC == "" {
		c.Tools.OC = "oc"
	}
	if c.Tools.Butane == "" {
		c.Tools.Butane = "butane"
	}
}

// Validate는 설정값의 필수 항목과 형식을 확인합니다.
func (c *Config) Validate() error {
	if c.Cluster.Domain == "" {
		return fmt.Errorf("cluster.domain이 비어 있습니다: %s", c.path)
	}
	if strings.ContainsAny(c.Cluster.Domain, " /:") {
		return fmt.Errorf("cluster.domain 형식이 올바르지 않습니다: %q", c.Cluster.Domain)
	}
	apiURL, err := url.Parse(c.Cluster.APIURL)
	if err != nil {
		return fmt.Errorf("cluster.apiURL 형식이 올바르지 않습니다: %q: %w", c.Cluster.APIURL, err)
	}
	// API URL의 호스트는 cluster.domain에 속해야 합니다. MCS 주소와 인증서는
	// cluster.domain에서 파생되므로, API URL이 다른 도메인을 가리키면 현재
	// 로그인한 클러스터와 Ignition 대상 클러스터의 일관성을 보장할 수 없습니다.
	if !strings.HasSuffix(strings.ToLower(apiURL.Hostname()), "."+strings.ToLower(c.Cluster.Domain)) {
		return fmt.Errorf(
			"cluster.apiURL의 호스트가 cluster.domain 소속이 아닙니다: %q (cluster.domain: %s) — apiURL이 다른 클러스터를 가리키면 노드 hostname 검증과 사전 점검이 서로 다른 클러스터를 보게 됩니다",
			c.Cluster.APIURL, c.Cluster.Domain)
	}
	if c.Registry.PullSecret == "" {
		return fmt.Errorf("registry.pullSecret이 비어 있습니다: %s", c.path)
	}
	if c.WebServer.URL == "" {
		return fmt.Errorf("webServer.url이 비어 있습니다: %s", c.path)
	}
	// 게시 URL은 iDRAC Virtual Media가 지원하는 HTTP 또는 HTTPS만 허용합니다.
	if u, err := url.Parse(c.WebServer.URL); err != nil || u.Host == "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("webServer.url은 http:// 또는 https://로 시작하는 절대 주소여야 합니다: %q", c.WebServer.URL)
	}
	if c.Pathsets.Active == "" {
		return fmt.Errorf("pathsets.active가 비어 있습니다: %s", c.path)
	}
	if strings.ContainsRune(c.Pathsets.Active, os.PathSeparator) || c.Pathsets.Active == ".." {
		return fmt.Errorf("pathsets.active는 디렉터리 이름 하나여야 합니다: %q", c.Pathsets.Active)
	}

	// 음수 시간은 time.Duration으로 바꾸면 즉시 만료된 제한 시간이나 대기가
	// 되어 오타가 조용히 이상 동작으로 이어집니다. 0은 기본값 적용을
	// 뜻하므로 허용합니다.
	for name, v := range map[string]int{
		"redfish.timeoutSeconds":       c.Redfish.TimeoutSeconds,
		"redfish.powerOffWaitSeconds":  c.Redfish.PowerOffWaitSeconds,
		"redfish.mediaSettleSeconds":   c.Redfish.MediaSettleSeconds,
		"redfish.attributeWaitSeconds": c.Redfish.AttributeWaitSeconds,
		"redfish.powerOnWaitSeconds":   c.Redfish.PowerOnWaitSeconds,
	} {
		if v < 0 {
			return fmt.Errorf("%s는 음수일 수 없습니다: %d", name, v)
		}
	}

	// tmpDir는 prepare 단계에서 os.RemoveAll로 디렉터리 전체가 삭제되는 경로입니다.
	// "." 이나 ".." 로 잘못 적으면 작업 디렉터리 전체나 그 상위가 사라지므로,
	// 실행 전에 작업 디렉터리 안쪽을 가리키는지 확인합니다.
	//
	// 다른 workspace 항목(fullISO 등)은 파일 하나를 다루는 경로라 이 검사를
	// 적용하지 않습니다. 삭제 범위가 넓은 tmpDir에만 제한을 둡니다.
	return validateRemovableDir("workspace.tmpDir", c.Workspace.TmpDir)
}

// validateRemovableDir는 전체가 삭제되는 디렉터리가 작업 디렉터리 하위인지 확인합니다.
// 절대 경로, 상위로 올라가는 경로, 현재 디렉터리 자신은 모두 거부합니다.
func validateRemovableDir(key, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s가 비어 있습니다", key)
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return fmt.Errorf("%s는 작업 디렉터리 기준 상대 경로여야 합니다: %q", key, value)
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." || cleaned == ".." ||
		cleaned == string(os.PathSeparator) ||
		strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s는 작업 디렉터리 안의 하위 경로여야 합니다: %q", key, value)
	}
	return nil
}

// insideWorkspace는 계산된 절대 경로가 문자열 기준으로 작업 디렉터리 아래에
// 있는지 확인합니다. 심볼릭 링크는 따라가지 않으므로, 삭제 대상 경로는
// 호출 측에서 링크를 해석한 뒤 다시 검사해야 합니다.
func insideWorkspace(key, dir, path string) error {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return fmt.Errorf("%s 경로를 확인하지 못했습니다: %s: %w", key, path, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s가 작업 디렉터리 밖을 가리킵니다: %s (작업 디렉터리: %s)", key, path, dir)
	}
	return nil
}

// Path는 읽어 들인 설정 파일 경로입니다.
func (c *Config) Path() string { return c.path }

// resolve는 설정 파일 기준 상대 경로를 절대 경로로 바꿉니다.
func (c *Config) resolve(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.baseDir, p)
}

// WorkspaceDir는 산출물이 쌓이는 절대 경로입니다.
// 비어 있으면 현재 작업 디렉터리를 사용합니다.
func (c *Config) WorkspaceDir() (string, error) {
	dir := c.Workspace.Dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("작업 디렉터리를 확인하지 못했습니다: %s: %w", dir, err)
	}
	return abs, nil
}

// TemplatePath는 worker pointer Ignition 템플릿의 절대 경로입니다.
func (c *Config) TemplatePath() string { return c.resolve(c.Templates.WorkerPointerIgnition) }

// PathsetsDir는 pathset 디렉터리들의 상위 디렉터리 절대 경로입니다.
func (c *Config) PathsetsDir() string { return c.resolve(c.Pathsets.Dir) }

// PullSecretPath는 pull secret의 절대 경로입니다.
func (c *Config) PullSecretPath() string { return c.resolve(c.Registry.PullSecret) }

// BaseRegistry는 레지스트리 주소에서 호스트 부분을 반환합니다.
func (c *Config) BaseRegistry() string {
	if i := strings.IndexByte(c.Registry.Registry, '/'); i >= 0 {
		return c.Registry.Registry[:i]
	}
	return c.Registry.Registry
}

// MCSEndpoint는 Machine Config Server 주소입니다. 예: api-int.mycluster.example.com:22623
func (c *Config) MCSEndpoint() string {
	return fmt.Sprintf("api-int.%s:%d", c.Cluster.Domain, c.Cluster.MCSPort)
}

// MCSSource는 Ignition merge에 사용할 worker config URL입니다.
func (c *Config) MCSSource() string {
	return fmt.Sprintf("https://%s/config/worker", c.MCSEndpoint())
}

// PublishURL은 웹 서버에 게시된 파일의 URL을 만듭니다.
func (c *Config) PublishURL(filename string) string {
	return c.WebServer.URL + c.WebServer.Path + "/" + filename
}

// RootfsURL은 minimal ISO의 커널 인자에 기록할 rootfs 주소입니다.
func (c *Config) RootfsURL() string { return c.PublishURL(c.Workspace.RootfsImage) }

// Paths는 작업 디렉터리 기준 산출물 경로 모음입니다.
type Paths struct {
	Dir         string // 작업 디렉터리
	TmpDir      string
	FullISO     string
	MinimalISO  string
	RootfsImage string
	// Installer는 coreos-installer 실행에 사용할 경로입니다. 외부 도구가
	// 설정되지 않았으면 작업 디렉터리에 추출한 바이너리를 사용합니다.
	Installer string
	// WorkspaceInstaller는 release 이미지에서 coreos-installer를 추출할
	// 작업 디렉터리 안의 경로입니다. 외부 도구 경로를 추출 대상으로 사용하면
	// 시스템 바이너리를 덮어쓸 수 있으므로 Installer와 구분합니다.
	WorkspaceInstaller string
}

// Paths는 작업 디렉터리를 기준으로 산출물 절대 경로를 계산합니다.
func (c *Config) Paths() (Paths, error) {
	dir, err := c.WorkspaceDir()
	if err != nil {
		return Paths{}, err
	}
	workspaceInstaller := filepath.Join(dir, c.Workspace.Installer)
	installer := c.Tools.CoreOSInstaller
	if installer == "" {
		installer = workspaceInstaller
	} else if !filepath.IsAbs(installer) {
		installer = c.resolve(installer)
	}
	paths := Paths{
		Dir:                dir,
		TmpDir:             filepath.Join(dir, c.Workspace.TmpDir),
		FullISO:            filepath.Join(dir, c.Workspace.FullISO),
		MinimalISO:         filepath.Join(dir, c.Workspace.MinimalISO),
		RootfsImage:        filepath.Join(dir, c.Workspace.RootfsImage),
		Installer:          installer,
		WorkspaceInstaller: workspaceInstaller,
	}

	// tmpDir는 디렉터리 전체가 삭제되므로 계산된 절대 경로도 다시 확인합니다.
	if err := insideWorkspace("workspace.tmpDir", dir, paths.TmpDir); err != nil {
		return Paths{}, err
	}
	// 중간 경로의 심볼릭 링크가 작업 디렉터리 밖을 가리킬 수 있으므로,
	// 현재 존재하는 경로 구성요소의 링크를 해석한 뒤 실제 위치도 확인합니다.
	resolvedDir, err := resolveExisting(dir)
	if err != nil {
		return Paths{}, fmt.Errorf("작업 디렉터리 경로를 해석하지 못했습니다: %s: %w", dir, err)
	}
	resolvedTmp, err := resolveExisting(paths.TmpDir)
	if err != nil {
		return Paths{}, fmt.Errorf("workspace.tmpDir 경로를 해석하지 못했습니다: %s: %w", paths.TmpDir, err)
	}
	if err := insideWorkspace("workspace.tmpDir(심볼릭 링크 해석 후)", resolvedDir, resolvedTmp); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

// resolveExisting은 경로에서 현재 존재하는 가장 긴 앞부분의 심볼릭 링크를
// 해석하고, 아직 존재하지 않는 나머지 경로를 결합해 반환합니다.
func resolveExisting(path string) (string, error) {
	remainder := ""
	cur := path
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, remainder), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		remainder = filepath.Join(filepath.Base(cur), remainder)
		cur = parent
	}
}

// NodeIgnition은 노드별 Ignition 파일 경로입니다.
func (p Paths) NodeIgnition(host string) string { return filepath.Join(p.Dir, host+".ign") }

// NodeNMState는 노드별 NMState YAML 경로입니다.
func (p Paths) NodeNMState(host string) string { return filepath.Join(p.Dir, host+".yaml") }

// NodeISO는 노드별 설치 ISO 경로입니다.
func (p Paths) NodeISO(host string) string { return filepath.Join(p.Dir, host+".iso") }
