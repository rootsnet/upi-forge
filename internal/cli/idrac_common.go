package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/config"
	"upi-forge/internal/csvdata"
	"upi-forge/internal/idrac"
	"upi-forge/internal/logx"
	"upi-forge/internal/prompt"
	"upi-forge/internal/redfish"
)

// flagError는 -h 출력은 정상 종료로, 나머지는 오류로 다룹니다.
func flagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

// bmcDriverFor는 pathset의 bmc.type에 맞는 드라이버를 선택합니다.
// idracs.csv 형식(hostname, address, username)은 장비 종류와 무관하게
// 공통이지만, 리소스 경로와 부팅 절차는 장비마다 다르므로 여기서 갈립니다.
// 각 명령이 선택된 드라이버를 실제로 호출하는지 가짜 드라이버로 고정하는
// 테스트를 위해 변수입니다.
var bmcDriverFor = resolveBMCDriver

// resolveBMCDriver는 bmc.type과 드라이버의 대응 목록입니다.
// 다른 장비를 추가할 때는 config.SupportedBMCTypes에 종류를 등록하고
// 여기에 그 장비 패키지의 드라이버를 연결합니다(bmc.Driver 참고).
// boot/eject/inventory 명령 도움말(usage)은 현재 유일한 지원 장비인
// iDRAC 기준으로 쓰여 있으므로, 그때 도움말도 함께 고쳐야 합니다.
// 알 수 없는 종류는 설정 검증(Pathset.Validate)이 먼저 거르므로
// 여기 도달하면 두 목록이 어긋난 것입니다.
func resolveBMCDriver(ps *config.Pathset) (*bmc.Driver, error) {
	switch ps.BMC.Type {
	case config.BMCTypeIDRAC10:
		return idrac.Driver(), nil
	default:
		return nil, fmt.Errorf("bmc.type %q의 구현이 없습니다 (선택 pathset: %s, 지원: %s)",
			ps.BMC.Type, ps.Name, strings.Join(config.SupportedBMCTypes, ", "))
	}
}

// idracTargets는 nodes.csv와 idracs.csv를 읽고 일대일 대응을 검증합니다.
func idracTargets(ps *config.Pathset, from string) (*csvdata.Nodes, *csvdata.IDRACs, []string, error) {
	nodes, err := csvdata.LoadNodes(ps.NodesCSV())
	if err != nil {
		return nil, nil, nil, err
	}
	idracs, err := csvdata.LoadIDRACs(ps.IDRACsCSV())
	if err != nil {
		return nil, nil, nil, err
	}
	if err := idracs.MatchNodes(nodes); err != nil {
		return nil, nil, nil, err
	}
	targets, err := nodes.From(from)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(targets) == 0 {
		return nil, nil, nil, fmt.Errorf("%s: 처리할 노드가 없습니다", ps.NodesCSV())
	}
	return nodes, idracs, targets, nil
}

// redfishOptions는 설정값을 Redfish 클라이언트 옵션으로 바꿉니다.
func redfishOptions(cfg *config.Config) redfish.Options {
	return redfish.Options{
		TLSVerify: cfg.Redfish.TLSVerify,
		Timeout:   time.Duration(cfg.Redfish.TimeoutSeconds) * time.Second,
	}
}

// waitsFrom은 설정값을 BMC 단계별 대기 시간으로 바꿉니다.
func waitsFrom(cfg *config.Config) bmc.Waits {
	return bmc.Waits{
		PowerOff:  time.Duration(cfg.Redfish.PowerOffWaitSeconds) * time.Second,
		Media:     time.Duration(cfg.Redfish.MediaSettleSeconds) * time.Second,
		Attribute: time.Duration(cfg.Redfish.AttributeWaitSeconds) * time.Second,
		PowerOn:   time.Duration(cfg.Redfish.PowerOnWaitSeconds) * time.Second,
	}
}

// nodeAction은 노드 하나에 대해 수행할 BMC 작업입니다.
type nodeAction func(ctx context.Context, host string, target csvdata.IDRAC, client *redfish.Client) error

// nodePreview는 비밀번호를 묻기 전에 이 노드에 무엇을 할지 보여줍니다.
// 운영자가 대상 장비와 ISO를 확인한 뒤 비밀번호를 입력할 수 있어야 하므로,
// 반드시 프롬프트보다 먼저 출력합니다.
type nodePreview func(host string, target csvdata.IDRAC)

// forEachBMCNode는 대상 노드를 순서대로 처리합니다.
//
// 노드마다 "무엇을 할지 출력 -> 비밀번호 숨김 입력 -> 세션 생성 -> 작업 -> 세션 정리"
// 순서로 진행합니다. 비밀번호를 한 번에 몰아서 받지 않는 이유는, 어떤 장비에
// 어떤 ISO를 붙이는지 확인한 뒤에 입력하도록 하기 위해서입니다.
//
// 중간에 실패하면 이미 성공한 노드의 요청은 취소하지 않고,
// 문제를 고친 뒤 이어서 실행할 명령을 안내합니다.
func forEachBMCNode(ctx context.Context, app *App, cfg *config.Config, idracs *csvdata.IDRACs,
	targets []string, commandName string, preview nodePreview, action nodeAction) error {

	for _, host := range targets {
		target, ok := idracs.Get(host)
		if !ok {
			return fmt.Errorf("idracs.csv에 없는 노드입니다: %s", host)
		}

		logx.Section("%s / %s", host, target.Address)
		if preview != nil {
			preview(host, target)
		}
		password, err := prompt.BMCPassword(host)
		if err != nil {
			resumeHint(app, commandName, host)
			return fmt.Errorf("%s: %w", host, err)
		}

		if err := withSession(ctx, cfg, target, password, func(client *redfish.Client) error {
			return action(ctx, host, target, client)
		}); err != nil {
			resumeHint(app, commandName, host)
			return fmt.Errorf("%s: %w", host, err)
		}
	}
	return nil
}

// withSession은 Redfish 세션을 열고 반드시 정리합니다.
// BMC 디스패치 테스트가 세션 생성 없이 작업만 실행하도록 변수입니다.
var withSession = func(ctx context.Context, cfg *config.Config, target csvdata.IDRAC,
	password string, fn func(client *redfish.Client) error) error {

	client := redfish.New(target.Address, redfishOptions(cfg))
	if err := client.Login(ctx, target.Username, password); err != nil {
		return err
	}
	defer client.Logout()
	return fn(client)
}

// resumeHint는 실패한 노드부터 이어서 실행할 명령을 안내합니다.
// --config, --pathset을 지정해 실행했다면 안내 명령에도 포함합니다.
func resumeHint(app *App, commandName, host string) {
	fmt.Fprintf(os.Stderr, "\n문제를 수정한 뒤 다음 명령으로 %s 노드부터 이어서 실행하세요:\n", host)
	fmt.Fprintf(os.Stderr, "  %s\n", app.ResumeCommand(commandName, host))
}
