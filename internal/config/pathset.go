package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"upi-forge/internal/yamlx"
)

// NetworkSource는 NMState YAML을 어떻게 준비할지 나타냅니다.
type NetworkSource string

const (
	// NetworkGenerate는 nodes.csv, nics.csv, pathset 설정으로 YAML을 생성합니다.
	NetworkGenerate NetworkSource = "generate"
	// NetworkCopy는 미리 준비한 <hostname>.yaml을 작업 디렉터리로 복제합니다.
	NetworkCopy NetworkSource = "copy"
)

// PathsetFileName은 pathset 디렉터리 안의 설정 파일 이름입니다.
const PathsetFileName = "pathset.yaml"

// BMCTypeIDRAC10은 현재 구현된 유일한 BMC 종류입니다.
// bmc.type을 생략하면 이 값이 기본입니다.
const BMCTypeIDRAC10 = "idrac10"

// SupportedBMCTypes는 이 바이너리가 구현한 BMC 종류 목록입니다.
// 다른 장비 지원을 추가할 때 여기와 cli의 구현 선택에 함께 항목을 추가합니다.
var SupportedBMCTypes = []string{BMCTypeIDRAC10}

// BMC는 idracs.csv의 관리 컨트롤러에 사용할 구현을 지정합니다.
// hostname/ip 형식의 CSV는 공통이고, Redfish 리소스 경로와 부팅 절차가
// 장비 종류에 따라 달라 구현 선택이 필요합니다.
type BMC struct {
	// Type은 BMC 종류입니다. 생략하면 idrac10입니다.
	Type string
}

// Pathset은 디스크 by-path와 NIC 구성이 같은 하드웨어 그룹 설정입니다.
// 기존 pathset-N/env.sh에 해당합니다.
type Pathset struct {
	Name        string
	Description string
	ISO         ISOSpec
	Disk        Disk
	// Butane은 pathset 디렉터리 기준 Butane 파일 경로입니다. 비우면 병합하지 않습니다.
	Butane  string
	Network Network
	Files   Files
	BMC     BMC

	dir string // pathset 디렉터리 절대 경로
}

// ISOSpec은 ISO 생성 기본 모드입니다.
type ISOSpec struct {
	// UseRootfs가 true면 minimal ISO와 분리 rootfs를 사용합니다.
	UseRootfs bool
}

// Disk는 OS 설치 대상 디스크입니다.
type Disk struct {
	// OSDisk는 RHCOS를 설치할 디스크의 실측 /dev/disk/by-path 값입니다.
	// Butane의 wipe 또는 RAID 대상에 절대 포함하면 안 됩니다.
	OSDisk string
}

// Network는 NMState 생성에 필요한 값입니다.
type Network struct {
	Source       NetworkSource
	SourceDir    string // source가 copy일 때 원본 YAML 디렉터리
	Gateway      string
	PrefixLength int
	DNS          []string
	Bonding      bool
	ActiveNIC    string
	Bond         Bond
}

// Bond는 active-backup bond 설정입니다.
type Bond struct {
	Name    string
	Mode    string
	Primary string
	Standby string
	MIIMon  int
}

// Files는 pathset 디렉터리 안의 CSV 파일 이름입니다.
type Files struct {
	Nodes  string
	NICs   string
	IDRACs string
}

// LoadPathset은 설정에서 선택한 pathset을 읽습니다.
func (c *Config) LoadPathset() (*Pathset, error) {
	return c.LoadPathsetNamed(c.Pathsets.Active)
}

// LoadPathsetNamed는 이름으로 pathset을 읽습니다.
// pathset 전용 명령(인벤토리, 라이브 부팅)에서 --pathset으로 다른 그룹을 지정할 때 사용합니다.
func (c *Config) LoadPathsetNamed(name string) (*Pathset, error) {
	if name == "" {
		return nil, fmt.Errorf("pathset 이름이 비어 있습니다")
	}
	if strings.ContainsRune(name, os.PathSeparator) || name == ".." {
		return nil, fmt.Errorf("pathset 이름은 디렉터리 이름 하나여야 합니다: %q", name)
	}
	dir := filepath.Join(c.PathsetsDir(), name)
	file := filepath.Join(dir, PathsetFileName)

	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("pathset 설정을 읽지 못했습니다: %s: %w", file, err)
	}
	root, err := yamlx.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("pathset 설정을 해석하지 못했습니다: %s: %w", file, err)
	}

	ps := &Pathset{dir: dir}
	if err := ps.decode(root); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if ps.Name == "" {
		ps.Name = name
	}
	if ps.Name != name {
		return nil, fmt.Errorf("%s: pathset 이름이 디렉터리와 다릅니다: 디렉터리=%s, name=%s", file, name, ps.Name)
	}
	ps.applyDefaults()
	if err := ps.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return ps, nil
}

func (p *Pathset) decode(root *yamlx.Node) error {
	m := yamlx.NewMapper(root)

	m.String("name", &p.Name)
	m.String("description", &p.Description)
	m.String("butane", &p.Butane)

	iso := m.Section("iso")
	iso.Bool("useRootfs", &p.ISO.UseRootfs)

	disk := m.Section("disk")
	disk.String("osDisk", &p.Disk.OSDisk)

	network := m.Section("network")
	var source string
	network.String("source", &source)
	p.Network.Source = NetworkSource(source)
	network.String("sourceDir", &p.Network.SourceDir)
	network.String("gateway", &p.Network.Gateway)
	network.Int("prefixLength", &p.Network.PrefixLength)
	network.Strings("dns", &p.Network.DNS)
	network.Bool("bonding", &p.Network.Bonding)
	network.String("activeNIC", &p.Network.ActiveNIC)

	bond := network.Section("bond")
	bond.String("name", &p.Network.Bond.Name)
	bond.String("mode", &p.Network.Bond.Mode)
	bond.String("primary", &p.Network.Bond.Primary)
	bond.String("standby", &p.Network.Bond.Standby)
	bond.Int("miimon", &p.Network.Bond.MIIMon)

	files := m.Section("files")
	files.String("nodes", &p.Files.Nodes)
	files.String("nics", &p.Files.NICs)
	files.String("idracs", &p.Files.IDRACs)

	bmc := m.Section("bmc")
	bmc.String("type", &p.BMC.Type)

	return m.Finish()
}

func (p *Pathset) applyDefaults() {
	if p.Network.Source == "" {
		p.Network.Source = NetworkGenerate
	}
	if p.Network.SourceDir == "" {
		p.Network.SourceDir = "network-yaml"
	}
	if p.Network.PrefixLength == 0 {
		p.Network.PrefixLength = 24
	}
	if p.Files.Nodes == "" {
		p.Files.Nodes = "nodes.csv"
	}
	if p.Files.NICs == "" {
		p.Files.NICs = "nics.csv"
	}
	if p.Files.IDRACs == "" {
		p.Files.IDRACs = "idracs.csv"
	}
	if p.BMC.Type == "" {
		p.BMC.Type = BMCTypeIDRAC10
	}
	if p.Network.Bonding {
		if p.Network.Bond.Mode == "" {
			p.Network.Bond.Mode = "active-backup"
		}
		if p.Network.Bond.MIIMon == 0 {
			p.Network.Bond.MIIMon = 100
		}
	}
	// 빈 DNS 항목은 제거합니다. 셸에서 DNS2/DNS3를 비워 두던 동작과 같습니다.
	dns := make([]string, 0, len(p.Network.DNS))
	for _, s := range p.Network.DNS {
		if s = strings.TrimSpace(s); s != "" {
			dns = append(dns, s)
		}
	}
	p.Network.DNS = dns
}

// Validate는 pathset 설정의 필수 항목과 형식을 확인합니다.
func (p *Pathset) Validate() error {
	if p.Disk.OSDisk == "" {
		return fmt.Errorf("disk.osDisk가 비어 있습니다")
	}
	// bmc.type 오타나 이 바이너리가 모르는 종류는 BMC 명령 실행 시점이
	// 아니라 설정 검증 시점에 잡습니다. VM 전용 pathset(idracs.csv 없음)도
	// 값을 넣었다면 유효해야 합니다.
	if !slices.Contains(SupportedBMCTypes, p.BMC.Type) {
		return fmt.Errorf("지원하지 않는 bmc.type입니다: %q (지원: %s)",
			p.BMC.Type, strings.Join(SupportedBMCTypes, ", "))
	}
	switch p.Network.Source {
	case NetworkGenerate, NetworkCopy:
	default:
		return fmt.Errorf("network.source는 generate 또는 copy만 사용할 수 있습니다: %q", p.Network.Source)
	}
	// network.dns 형식은 copy 모드에서도 항목이 있을 때 확인합니다.
	// copy pathset은 네트워크 값 검증을 건너뛰지만 ignition의 DNS 등록
	// 검증(copy에서는 선택 사항)이 이 목록을 쓰므로, 형식 오류를
	// ignition 실행 시점이 아니라 설정 검증 시점에 잡습니다.
	for _, s := range p.Network.DNS {
		if net.ParseIP(s) == nil {
			return fmt.Errorf("network.dns 항목이 유효한 IP가 아닙니다: %q", s)
		}
	}
	if p.Network.Source != NetworkGenerate {
		return nil
	}

	if p.Network.Gateway == "" {
		return fmt.Errorf("network.gateway가 비어 있습니다")
	}
	// 참고: net.ParseIP는 IPv6 주소도 통과시키며, 이는 의도된 동작입니다
	// (수작업 구성을 막지 않기로 확정된 운영 정책입니다).
	// 다만 generate 모드의 NMState 생성기는 IPv4 전용 YAML(ipv4 블록,
	// 0.0.0.0/0 경로)을 만들므로, 여기에 IPv6 값을 넣으면 그대로 ipv4
	// 필드에 들어가 노드에서 적용에 실패합니다. IPv6 구성이 필요하면
	// network.source: copy로 준비한 YAML을 사용하십시오(네트워크 값 검증도
	// 건너뜁니다).
	if net.ParseIP(p.Network.Gateway) == nil {
		return fmt.Errorf("network.gateway가 유효한 IP가 아닙니다: %q", p.Network.Gateway)
	}
	if p.Network.PrefixLength < 1 || p.Network.PrefixLength > 32 {
		return fmt.Errorf("network.prefixLength는 1~32 사이여야 합니다: %d", p.Network.PrefixLength)
	}
	if len(p.Network.DNS) == 0 {
		return fmt.Errorf("network.dns에 최소 한 개의 DNS 서버가 필요합니다")
	}
	if !p.Network.Bonding {
		if p.Network.ActiveNIC == "" {
			return fmt.Errorf("network.bonding이 false이면 network.activeNIC이 필요합니다")
		}
		return nil
	}

	b := p.Network.Bond
	if b.Name == "" || b.Primary == "" || b.Standby == "" {
		return fmt.Errorf("network.bonding이 true이면 bond.name, bond.primary, bond.standby가 필요합니다")
	}
	if b.Primary == b.Standby {
		return fmt.Errorf("bond.primary와 bond.standby는 달라야 합니다: %q", b.Primary)
	}
	if b.Name == b.Primary || b.Name == b.Standby {
		return fmt.Errorf("bond.name은 물리 NIC 이름과 달라야 합니다: %q", b.Name)
	}
	// NMState 생성기는 active-backup 전제(primary 지정)로 YAML을 만듭니다.
	// 다른 모드나 오타를 그대로 두면 생성은 성공하고 노드 부팅 후 NMState
	// 적용 단계에서 실패하므로 설정 검증 시점에 잡습니다.
	if b.Mode != "active-backup" {
		return fmt.Errorf("bond.mode는 active-backup만 지원합니다: %q", b.Mode)
	}
	if b.MIIMon < 1 {
		return fmt.Errorf("bond.miimon은 1 이상이어야 합니다: %d", b.MIIMon)
	}
	return nil
}

// Dir는 pathset 디렉터리 절대 경로입니다.
func (p *Pathset) Dir() string { return p.dir }

// NodesCSV는 nodes.csv 절대 경로입니다.
func (p *Pathset) NodesCSV() string { return filepath.Join(p.dir, p.Files.Nodes) }

// NICsCSV는 nics.csv 절대 경로입니다.
func (p *Pathset) NICsCSV() string { return filepath.Join(p.dir, p.Files.NICs) }

// IDRACsCSV는 idracs.csv 절대 경로입니다.
func (p *Pathset) IDRACsCSV() string { return filepath.Join(p.dir, p.Files.IDRACs) }

// ButanePath는 Butane 파일 절대 경로입니다. 설정하지 않았으면 빈 문자열입니다.
func (p *Pathset) ButanePath() string {
	if p.Butane == "" {
		return ""
	}
	if filepath.IsAbs(p.Butane) {
		return filepath.Clean(p.Butane)
	}
	return filepath.Join(p.dir, p.Butane)
}

// NetworkYAMLDir는 network.source가 copy일 때 원본 YAML 디렉터리입니다.
func (p *Pathset) NetworkYAMLDir() string {
	if filepath.IsAbs(p.Network.SourceDir) {
		return filepath.Clean(p.Network.SourceDir)
	}
	return filepath.Join(p.dir, p.Network.SourceDir)
}

// InventoryDir는 iDRAC 인벤토리 결과를 저장할 디렉터리입니다.
func (p *Pathset) InventoryDir() string { return filepath.Join(p.dir, "inventory") }

// RouteInterface는 기본 경로의 next-hop 인터페이스 이름입니다.
func (p *Pathset) RouteInterface() string {
	if p.Network.Bonding {
		return p.Network.Bond.Name
	}
	return p.Network.ActiveNIC
}
