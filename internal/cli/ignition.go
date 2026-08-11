package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/dnscheck"
	"upi-forge/internal/fsutil"
	"upi-forge/internal/ignition"
	"upi-forge/internal/logx"
	"upi-forge/internal/nmstate"
	"upi-forge/internal/preflight"
)

const ignitionUsage = `upi-forge ignition - 노드별 NMState YAML과 Ignition을 생성합니다.

기존 02_execution_ignition.sh에 해당합니다.

선택한 pathset의 nodes.csv 전체에 대해 작업 디렉터리에
<hostname>.yaml과 <hostname>.ign을 만듭니다.

NMState YAML 원본은 pathset의 network.source로 지정합니다.
  generate : nodes.csv, nics.csv, pathset 설정으로 생성합니다.
  copy     : pathset의 network-yaml/<hostname>.yaml을 복제합니다.

안전 규칙:
  - 기존 Ignition이 있으면 덮어쓰지 않고 중단합니다.
  - 기존 NMState YAML은 내용이 같을 때만 재사용하고, 다르면 중단합니다.
  - NMState YAML과 Ignition을 만들기 전에 OpenShift 사전 점검을 통과해야 합니다.
  - 생성 전에 노드의 DNS 등록을 pathset의 network.dns 서버로 검증합니다.

DNS 검증:
  nodes.csv의 hostname이 FQDN이면 그대로, 아니면 hostname.<cluster.domain>
  임시 FQDN으로 정방향(A)과 역방향(PTR)을 조회합니다.
  FQDN hostname은 도메인이 cluster.domain과 일치해야 합니다. 다른
  클러스터의 nodes.csv가 남아 있는 사고를 막는 검사이며, DNS 조회와
  무관하므로 --skip-dns-check로도 건너뛸 수 없습니다.
  정방향은 nodes.csv의 IP와 일치해야 하고, 역방향은 PTR 레코드가 있을 때만
  FQDN과 일치해야 합니다(PTR이 없으면 건너뜁니다).
  조회는 pathset의 network.dns 서버에만 직접 보내며(/etc/hosts와 search
  도메인 미적용) 응답하는 모든 서버를 검사합니다.
  network.dns가 없는 copy 모드 pathset은 경고 후 검증을 건너뜁니다.
  --skip-dns-check 옵션으로 검증을 건너뛸 수 있습니다.`

func runIgnition(ctx context.Context, app *App, args []string) error {
	fs := newFlagSet("ignition", ignitionUsage)
	skipDNSCheck := fs.Bool("skip-dns-check", false, "노드 DNS 등록 검증을 건너뜁니다")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("인자를 지원하지 않습니다. 대상 노드는 pathset의 nodes.csv로 결정됩니다: %s", fs.Arg(0))
	}

	cfg, ps, err := app.Pathset()
	if err != nil {
		return err
	}
	paths, err := cfg.Paths()
	if err != nil {
		return err
	}

	nodes, err := csvdata.LoadNodes(ps.NodesCSV())
	if err != nil {
		return err
	}
	targets := nodes.Hostnames()

	// FQDN hostname은 클러스터 도메인 소속이어야 합니다. 다른 클러스터의
	// nodes.csv가 남아 있으면 DNS 정방향 검증까지 통과할 수 있으므로
	// (그 클러스터의 노드라서 DNS에 실제로 등록되어 있음), DNS 조회와
	// 무관한 이 검사는 --skip-dns-check로도 건너뛸 수 없습니다.
	if err := checkNodeDomains(ps.Name, nodes, cfg.Cluster.Domain); err != nil {
		return err
	}

	// 노드별 작업을 시작하기 전에 공통 입력과 출력 충돌을 모두 확인합니다.
	templatePath := cfg.TemplatePath()
	template, err := ignition.LoadTemplate(templatePath)
	if err != nil {
		return err
	}
	for _, host := range targets {
		if err := fsutil.MustNotExist(paths.NodeIgnition(host)); err != nil {
			return err
		}
	}

	// 산출물을 만들기 전에 노드의 DNS 등록을 검증합니다.
	// 노드가 실제로 사용할 pathset의 network.dns 서버로 직접 조회합니다.
	if *skipDNSCheck {
		logx.Warn("--skip-dns-check로 노드 DNS 등록 검증을 건너뜁니다.")
	} else if len(ps.Network.DNS) == 0 {
		// copy 모드 pathset은 네트워크 값 검증을 건너뛰므로 network.dns가
		// 없을 수 있습니다(generate 모드는 설정 검증이 필수로 요구합니다).
		// 없으면 오류가 아니라 건너뛰고, copy에서도 검증하려면 network.dns를
		// 설정하도록 안내합니다.
		logx.Warn("pathset에 network.dns가 없어 노드 DNS 등록 검증을 건너뜁니다.")
		logx.Warn("copy 모드에서도 검증하려면 pathset.yaml에 network.dns를 설정하세요.")
	} else {
		logx.Section("DNS 등록 검증")
		checker := &dnscheck.Checker{
			Servers: dnscheck.NewServers(ps.Network.DNS),
			Domain:  cfg.Cluster.Domain,
		}
		checkNodes := make([]dnscheck.Node, 0, len(nodes.Items))
		for _, node := range nodes.Items {
			checkNodes = append(checkNodes, dnscheck.Node{Hostname: node.Hostname, IP: node.IP})
		}
		if err := checker.Check(ctx, checkNodes); err != nil {
			return err
		}
	}

	// NMState YAML이나 Ignition을 만들기 전에 대상 클러스터를 확인합니다.
	if _, err := preflight.Check(ctx, cfg.Tools.OC, cfg.Cluster.APIURL); err != nil {
		return err
	}

	if err := prepareNMState(cfg, ps, nodes, targets, paths); err != nil {
		return err
	}
	for _, host := range targets {
		if !fsutil.IsRegularFile(paths.NodeNMState(host)) {
			return fmt.Errorf("작업 디렉터리에 NMState YAML이 없습니다: %s", paths.NodeNMState(host))
		}
	}

	logx.KeyValues(
		"선택 pathset", ps.Name,
		"DOMAIN", cfg.Cluster.Domain,
		"TEMPLATE", templatePath,
		"NETWORK", string(ps.Network.Source),
		"대상 노드", fmt.Sprint(targets),
	)

	// MCS 연결과 CA 수집은 실행당 한 번만 수행하고 모든 노드에 같은 체인을 사용합니다.
	// CA는 저장소나 파일로 남기지 않고 메모리에서만 다룹니다.
	endpoint := cfg.MCSEndpoint()
	if err := ignition.CheckMCS(ctx, endpoint); err != nil {
		return err
	}
	caSource, err := ignition.FetchCASource(ctx, endpoint)
	if err != nil {
		return err
	}

	butanePath := ps.ButanePath()
	if butanePath != "" && !fsutil.IsRegularFile(butanePath) {
		return fmt.Errorf("pathset의 Butane 파일을 읽을 수 없습니다: %s", butanePath)
	}

	for _, host := range targets {
		logx.Section("Ignition 준비: %s", host)

		doc, err := template.Render(ignition.RenderOptions{
			MCSSource: cfg.MCSSource(),
			MCSCA:     caSource,
			Hostname:  host,
		})
		if err != nil {
			return fmt.Errorf("%s: %w", host, err)
		}
		if butanePath != "" {
			logx.Info("정보: pathset Butane 병합: %s", butanePath)
			if err := doc.MergeButane(ctx, cfg.Tools.Butane, butanePath); err != nil {
				return fmt.Errorf("%s: %w", host, err)
			}
		} else {
			logx.Info("정보: pathset Butane 없음")
		}
		if err := doc.Validate(cfg.MCSSource()); err != nil {
			return fmt.Errorf("%s: %w", host, err)
		}

		data, err := doc.Bytes()
		if err != nil {
			return fmt.Errorf("%s: %w", host, err)
		}
		target := paths.NodeIgnition(host)
		if err := fsutil.MustNotExist(target); err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomic(target, data, 0o600); err != nil {
			return err
		}
		logx.Info("Ignition 파일 생성 완료: %s", filepath.Base(target))
		logx.Info("NMState 파일 준비됨: %s", paths.NodeNMState(host))
	}

	logx.Blank()
	logx.Info("완료: Ignition 준비가 끝났습니다.")
	return nil
}

// prepareNMState는 pathset 설정에 따라 노드별 NMState YAML을 작업 디렉터리에 준비합니다.
func prepareNMState(cfg *config.Config, ps *config.Pathset, nodes *csvdata.Nodes,
	targets []string, paths config.Paths) error {

	if ps.Network.Source == config.NetworkCopy {
		return copyNMState(ps, targets, paths)
	}

	nics, err := csvdata.LoadNICs(ps.NICsCSV())
	if err != nil {
		return err
	}
	if err := csvdata.ValidateNICSelection(nics, ps.Network.Bonding,
		ps.Network.ActiveNIC, ps.Network.Bond.Primary, ps.Network.Bond.Standby); err != nil {
		return err
	}

	// 이전 흐름에서 pathset 디렉터리에 만들어 둔 YAML은 혼용을 막기 위해 자동 이관하지 않습니다.
	for _, host := range targets {
		legacy := filepath.Join(ps.Dir(), host+".yaml")
		if fsutil.Exists(legacy) {
			return fmt.Errorf("이전 흐름의 pathset NMState YAML이 있습니다. 내용을 검토하고 직접 정리하세요: %s", legacy)
		}
	}

	// 모든 노드의 YAML을 먼저 만들어 두고, 충돌이 없을 때만 반영합니다.
	rendered := make(map[string][]byte, len(targets))
	for _, host := range targets {
		ip, ok := nodes.IP(host)
		if !ok {
			return fmt.Errorf("nodes.csv에서 노드 IP를 찾지 못했습니다: %s", host)
		}
		data, err := nmstate.Generate(nmstate.Input{
			NICs:    nics.Items,
			IP:      ip,
			Network: ps.Network,
		})
		if err != nil {
			return fmt.Errorf("%s: NMState 생성에 실패했습니다: %w", host, err)
		}
		rendered[host] = data
	}
	for _, host := range targets {
		target := paths.NodeNMState(host)
		if !fsutil.Exists(target) {
			continue
		}
		existing, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("기존 NMState YAML을 읽지 못했습니다: %s: %w", target, err)
		}
		if string(existing) != string(rendered[host]) {
			return fmt.Errorf("기존 NMState YAML의 내용이 달라 덮어쓰지 않습니다: %s", target)
		}
	}
	for _, host := range targets {
		target := paths.NodeNMState(host)
		reused, err := fsutil.WriteFileIfSameOrNew(target, rendered[host], 0o644)
		if err != nil {
			return err
		}
		ip, _ := nodes.IP(host)
		if reused {
			logx.Info("재사용: %s (내용 동일)", target)
			continue
		}
		logx.Info("생성: %s (ip=%s)", target, ip)
	}
	return nil
}

// copyNMState는 baremetal 등 외부에서 만든 YAML을 작업 디렉터리로 복제합니다.
func copyNMState(ps *config.Pathset, targets []string, paths config.Paths) error {
	sourceDir := ps.NetworkYAMLDir()
	for _, host := range targets {
		source := filepath.Join(sourceDir, host+".yaml")
		if !fsutil.IsRegularFile(source) {
			return fmt.Errorf("복제할 NMState YAML을 읽을 수 없습니다: %s", source)
		}
		target := paths.NodeNMState(host)
		if fsutil.Exists(target) {
			same, err := fsutil.SameContent(source, target)
			if err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("기존 NMState YAML의 내용이 달라 덮어쓰지 않습니다: %s", target)
			}
		}
	}
	for _, host := range targets {
		source := filepath.Join(sourceDir, host+".yaml")
		target := paths.NodeNMState(host)
		if fsutil.Exists(target) {
			logx.Info("재사용: %s (복제 원본과 내용 동일)", target)
			continue
		}
		if err := fsutil.CopyFile(source, target, 0o644); err != nil {
			return err
		}
		logx.Info("복제: %s -> %s", source, target)
	}
	return nil
}
