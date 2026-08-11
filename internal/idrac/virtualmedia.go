package idrac

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

// VirtualMedia는 VirtualMedia 리소스에서 사용하는 필드입니다.
type VirtualMedia struct {
	ID             string   `json:"Id"`
	MediaTypes     []string `json:"MediaTypes"`
	Inserted       bool     `json:"Inserted"`
	Image          string   `json:"Image"`
	ImageName      string   `json:"ImageName"`
	ConnectedVia   string   `json:"ConnectedVia"`
	WriteProtected bool     `json:"WriteProtected"`
	Actions        struct {
		Insert struct {
			Target string `json:"target"`
		} `json:"#VirtualMedia.InsertMedia"`
		Eject struct {
			Target string `json:"target"`
		} `json:"#VirtualMedia.EjectMedia"`
	} `json:"Actions"`

	uri string // 이 리소스의 @odata.id
}

// URI는 리소스 경로입니다.
func (v *VirtualMedia) URI() string { return v.uri }

// SupportsOpticalInsert는 CD/DVD를 넣을 수 있는 장치인지 확인합니다.
func (v *VirtualMedia) SupportsOpticalInsert() bool {
	if v.Actions.Insert.Target == "" {
		return false
	}
	for _, t := range v.MediaTypes {
		if t == "CD" || t == "DVD" {
			return true
		}
	}
	return false
}

// ListVirtualMedia는 Virtual Media 장치를 URI의 번호순으로 정렬해 반환합니다.
// Redfish 컬렉션의 구성원 순서는 보장되지 않으므로 명시적으로 정렬합니다.
func ListVirtualMedia(ctx context.Context, c *redfish.Client) ([]*VirtualMedia, error) {
	var collection redfish.Collection
	if err := c.Get(ctx, SystemPath+"/VirtualMedia", &collection); err != nil {
		return nil, fmt.Errorf("Virtual Media 장치 목록을 조회하지 못했습니다: %w", err)
	}
	uris := collection.IDs()
	if len(uris) == 0 {
		return nil, fmt.Errorf("Virtual Media 장치를 찾지 못했습니다")
	}
	sort.Strings(uris)

	out := make([]*VirtualMedia, 0, len(uris))
	for _, uri := range uris {
		var media VirtualMedia
		if err := c.Get(ctx, uri, &media); err != nil {
			return nil, fmt.Errorf("Virtual Media 장치를 조회하지 못했습니다: %s: %w", uri, err)
		}
		media.uri = uri
		out = append(out, &media)
	}
	return out, nil
}

// BootOption은 부트 항목 리소스입니다.
type BootOption struct {
	DisplayName string        `json:"DisplayName"`
	RelatedItem []redfish.Ref `json:"RelatedItem"`
}

var virtualOpticalName = regexp.MustCompile(`(?i)virtual (optical|cd|dvd)`)

// isVirtualMediaBootOption은 부트 항목이 가상 미디어인지 판단합니다.
func isVirtualMediaBootOption(o BootOption) bool {
	for _, item := range o.RelatedItem {
		if strings.Contains(item.ID, "VirtualMedia") {
			return true
		}
	}
	return virtualOpticalName.MatchString(o.DisplayName)
}

// CheckISOURL은 iDRAC에 마운트하기 전에 현재 호스트에서 ISO URL의 앞부분을
// 읽을 수 있는지 확인합니다. HTTP 상태가 200 또는 206이면 성공입니다.
//
// 웹 서버 구성을 지원하기 위해 HTTP 리다이렉트를 최대 10회까지 허용합니다.
// 다만 실제 ISO 스트리밍은 iDRAC 펌웨어가 수행하며, 펌웨어의 리다이렉트
// 처리 여부는 장비마다 다를 수 있습니다. 사전 확인이 성공해도 마운트가
// 실패하면 리다이렉트 없는 직접 URL을 사용해 보십시오.
func CheckISOURL(ctx context.Context, isoURL string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:           nil, // 프록시를 거치지 않고 직접 확인합니다.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, isoURL, nil)
	if err != nil {
		return fmt.Errorf("ISO URL 형식이 올바르지 않습니다: %s: %w", isoURL, err)
	}
	req.Header.Set("Range", "bytes=0-1023")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ISO 파일에 접근할 수 없습니다: %s\n"+
			"웹 서버 상태, 파일 경로, 방화벽 설정을 확인하세요: %w", isoURL, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return nil
	default:
		return fmt.Errorf("ISO 파일에 접근할 수 없습니다 (HTTP %d): %s\n"+
			"웹 서버 권한, 파일 경로, SELinux 설정을 확인하세요", resp.StatusCode, isoURL)
	}
}

// powerOffPollInterval은 전원 꺼짐 확인의 폴링 간격입니다.
// 테스트에서 짧게 바꿉니다.
var powerOffPollInterval = 2 * time.Second

// waitPowerOff는 PowerState가 Off가 될 때까지 limit 안에서 폴링합니다.
//
// limit는 폴링 사이의 대기뿐 아니라 상태 조회(GET) 자체에도 걸리는
// 이 단계 전체의 상한입니다. 응답이 멈춘 BMC 때문에 HTTP 제한 시간
// (redfish.timeoutSeconds)까지 붙들리지 않습니다.
//
// 어떤 경우에도 흐름을 막지 않습니다: 제한 시간 초과, 알 수 없는 상태,
// 상태 조회 실패는 모두 경고 후 통과입니다(fail-open). 유일한 오류는
// 컨텍스트 취소(Ctrl+C)입니다.
func waitPowerOff(ctx context.Context, c *redfish.Client, limit time.Duration) error {
	pollCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	deadline, _ := pollCtx.Deadline()

	state := "알 수 없음"
	for {
		sys, err := GetSystem(pollCtx, c)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if pollCtx.Err() != nil {
				logx.Warn("제한 시간 안에 전원 꺼짐이 확인되지 않았습니다(마지막 확인: %s). 계속 진행합니다.",
					state)
				return nil
			}
			logx.Warn("전원 상태를 확인하지 못했습니다. 계속 진행합니다: %v", err)
			return nil
		}
		state = sys.PowerState
		if state == "Off" {
			logx.Info("현재 전원 상태: %s", state)
			return nil
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			logx.Warn("제한 시간 안에 전원 꺼짐이 확인되지 않았습니다(현재: %s). 계속 진행합니다.", state)
			return nil
		}
		if err := sleep(pollCtx, min(powerOffPollInterval, remain)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logx.Warn("제한 시간 안에 전원 꺼짐이 확인되지 않았습니다(현재: %s). 계속 진행합니다.", state)
			return nil
		}
	}
}

// Boot는 지정한 ISO로 서버를 원타임 가상 미디어 부팅합니다.
func Boot(ctx context.Context, c *redfish.Client, req bmc.BootRequest) error {
	if req.ISOURL == "" {
		return fmt.Errorf("마운트할 ISO URL이 비어 있습니다")
	}
	if req.HTTPTimeout <= 0 {
		req.HTTPTimeout = 60 * time.Second
	}

	// 1. ISO 파일 접근 확인
	// iDRAC이 ISO를 읽기 전에, 이 호스트에서 먼저 URL이 열리는지 확인합니다.
	// (ISO 주소는 호출 측에서 이미 출력했으므로 여기서는 결과만 알립니다.)
	if !req.SkipISOCheck {
		if err := CheckISOURL(ctx, req.ISOURL, req.HTTPTimeout); err != nil {
			return err
		}
		logx.Info("ISO 파일 접근 성공")
	}

	// 2~3. Virtual Media 장치 목록 조회와 선택
	devices, err := ListVirtualMedia(ctx, c)
	if err != nil {
		return err
	}
	var selected *VirtualMedia
	for _, device := range devices {
		if device.SupportsOpticalInsert() {
			logx.Debug("장치 %s: %v 사용 가능", device.ID, device.MediaTypes)
			if selected == nil {
				selected = device
			}
			continue
		}
		logx.Debug("장치 %s: %v 사용 불가 (건너뜀)", device.ID, device.MediaTypes)
	}
	if selected == nil {
		return fmt.Errorf("ISO를 넣을 수 있는 Virtual Media 장치가 없습니다")
	}
	logx.Info("선택된 Virtual Media 장치: %s", selected.ID)

	// 4. 서버 전원 OFF
	//
	// 전원 OFF 후에는 표준 ComputerSystem.PowerState를 폴링해 꺼짐을
	// 확인합니다. Off가 확인되면 즉시 다음 단계로 진행하고, 제한 시간
	// (redfish.powerOffWaitSeconds) 안에 확인되지 않아도 실패하지 않고 경고
	// 후 진행합니다 — 최종 PowerState를 성공 조건으로 강제하지 않던 동작과
	// 최악의 경우가 같도록 유지하는 확정된 운영 정책(fail-open)입니다.
	// 전원 ON 쪽은 부팅 완료를 기다릴 이유가 없으므로 고정 대기를 유지합니다.
	logx.Info("서버 전원을 끄고 꺼짐을 확인합니다. (최대 %s)", req.Waits.PowerOff)
	if err := Reset(ctx, c, "ForceOff"); err != nil {
		return err
	}
	if err := waitPowerOff(ctx, c, req.Waits.PowerOff); err != nil {
		return err
	}

	// 5. 기존 Virtual Media 모두 제거
	// 비어 있는 장치에 EjectMedia를 호출하면 VRM0009 / HTTP 500이 나므로
	// 반드시 Inserted 상태를 먼저 확인합니다.
	if err := ejectAll(ctx, c, devices, req.Waits.Media); err != nil {
		return err
	}

	// 5-1. Virtual Media 안정성 설정
	// 대용량 live ISO를 마운트할 때 부팅 도중 미디어가 끊기는 문제를 줄입니다.
	//   Attached      : AutoAttach 상태에서 유휴 시 분리되는 것을 막습니다.
	//   EncryptEnable : BMC 실시간 암호화 부하로 세션이 끊기는 것을 막습니다.
	// 주의: EncryptEnable을 끄면 가상 미디어 트래픽이 암호화되지 않습니다.
	//       신뢰할 수 없는 네트워크에서는 사용하지 마십시오.
	if err := SetAttributes(ctx, c, map[string]string{
		fmt.Sprintf("VirtualMedia.%s.Attached", selected.ID):      "Attached",
		fmt.Sprintf("VirtualMedia.%s.EncryptEnable", selected.ID): "Disabled",
	}); err != nil {
		return err
	}
	if err := sleep(ctx, req.Waits.Attribute); err != nil {
		return err
	}
	if attrs, err := GetAttributes(ctx, c); err == nil {
		logx.Info("Virtual Media 속성: Attached=%s, EncryptEnable=%s",
			attrs.String(fmt.Sprintf("VirtualMedia.%s.Attached", selected.ID)),
			attrs.String(fmt.Sprintf("VirtualMedia.%s.EncryptEnable", selected.ID)))
	}

	// 5-2. 부트 순서에서 Virtual CD/DVD 제거
	if err := removeVirtualMediaFromBootOrder(ctx, c); err != nil {
		return err
	}

	// 6. ISO 마운트
	logx.Info("ISO를 마운트합니다.")
	if _, err := c.Post(ctx, selected.URI()+"/Actions/VirtualMedia.InsertMedia", map[string]any{
		"Image":          req.ISOURL,
		"Inserted":       true,
		"WriteProtected": true,
	}); err != nil {
		return fmt.Errorf("ISO 마운트에 실패했습니다: %w", err)
	}
	if err := sleep(ctx, req.Waits.Media); err != nil {
		return err
	}

	// 7. ISO 마운트 결과 확인
	var mounted VirtualMedia
	if err := c.Get(ctx, selected.URI(), &mounted); err != nil {
		return fmt.Errorf("ISO 마운트 결과를 확인하지 못했습니다: %w", err)
	}
	logx.Info("마운트 상태: Inserted=%t, ConnectedVia=%s, ImageName=%s",
		mounted.Inserted, mounted.ConnectedVia, mounted.ImageName)
	if !mounted.Inserted || mounted.ConnectedVia != "URI" || mounted.Image != req.ISOURL {
		return fmt.Errorf("ISO 마운트 확인에 실패했습니다: Inserted=%t, ConnectedVia=%s, Image=%s",
			mounted.Inserted, mounted.ConnectedVia, mounted.Image)
	}
	logx.Info("ISO 마운트 확인 성공")

	// 8. 다음 1회 부팅을 Virtual CD/DVD로 설정
	// iDRAC10은 표준 BootSourceOverrideTarget으로는 가상 CD 부팅이 걸리지 않습니다.
	if err := SetAttributes(ctx, c, map[string]string{
		"ServerBoot.1.FirstBootDevice":                       "VCD-DVD",
		"ServerBoot.1.BootOnce":                              "Enabled",
		fmt.Sprintf("VirtualMedia.%s.BootOnce", selected.ID): "Enabled",
	}); err != nil {
		return err
	}
	if err := sleep(ctx, req.Waits.Attribute); err != nil {
		return err
	}

	// 9. 원타임 부트 설정 결과 확인
	attrs, err := GetAttributes(ctx, c)
	if err != nil {
		return fmt.Errorf("원타임 부트 설정을 확인하지 못했습니다: %w", err)
	}
	first := attrs.String("ServerBoot.1.FirstBootDevice")
	bootOnce := attrs.String("ServerBoot.1.BootOnce")
	mediaBootOnce := attrs.String(fmt.Sprintf("VirtualMedia.%s.BootOnce", selected.ID))
	logx.Info("부트 설정: FirstBootDevice=%s, BootOnce=%s, VirtualMedia.BootOnce=%s",
		first, bootOnce, mediaBootOnce)
	if first != "VCD-DVD" || bootOnce != "Enabled" || mediaBootOnce != "Enabled" {
		return fmt.Errorf("원타임 Virtual CD/DVD 부트 설정 확인에 실패했습니다")
	}
	logx.Info("원타임 Virtual CD/DVD 부트 설정 확인 성공")

	// 10. 서버 전원 ON
	logx.Info("서버 전원을 켜고 %s 기다립니다.", req.Waits.PowerOn)
	if err := Reset(ctx, c, "On"); err != nil {
		return err
	}
	if err := sleep(ctx, req.Waits.PowerOn); err != nil {
		return err
	}

	// 11. 최종 상태 확인
	sys, err := GetSystem(ctx, c)
	if err != nil {
		return fmt.Errorf("최종 상태를 확인하지 못했습니다: %w", err)
	}
	logx.Info("최종 상태: PowerState=%s, Health=%s", sys.PowerState, sys.Status.Health)
	logx.Info("주의: iDRAC10에서는 BootSourceOverrideTarget이 null로 표시될 수 있습니다.")
	logx.Info("      실제 ISO 부팅 여부는 iDRAC Virtual Console에서 최종 확인하세요.")
	logx.Info("      설치가 끝나면 `upi-forge eject`로 미디어를 제거하세요.")
	return nil
}

// ejectAll은 삽입된 미디어를 모두 제거합니다.
func ejectAll(ctx context.Context, c *redfish.Client, devices []*VirtualMedia, wait time.Duration) error {
	for _, device := range devices {
		var current VirtualMedia
		if err := c.Get(ctx, device.URI(), &current); err != nil {
			return fmt.Errorf("Virtual Media 상태를 확인하지 못했습니다: %s: %w", device.URI(), err)
		}
		if !current.Inserted {
			logx.Debug("장치 %s: 연결된 미디어가 없습니다.", device.ID)
			continue
		}
		logx.Info("장치 %s의 미디어를 제거합니다. (ImageName=%s)", device.ID, current.ImageName)
		if _, err := c.Post(ctx, device.URI()+"/Actions/VirtualMedia.EjectMedia", map[string]any{}); err != nil {
			return fmt.Errorf("Virtual Media 제거에 실패했습니다: %s: %w", device.ID, err)
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
	return nil
}

// Eject는 삽입된 모든 Virtual Media를 제거하고 결과를 검증합니다.
func Eject(ctx context.Context, c *redfish.Client, wait time.Duration) error {
	devices, err := ListVirtualMedia(ctx, c)
	if err != nil {
		return err
	}
	ejected := 0
	for _, device := range devices {
		var current VirtualMedia
		if err := c.Get(ctx, device.URI(), &current); err != nil {
			return fmt.Errorf("Virtual Media 상태를 확인하지 못했습니다: %s: %w", device.URI(), err)
		}
		logx.Info("장치 %s: Inserted=%t, ImageName=%s", device.ID, current.Inserted, orDash(current.ImageName))
		if !current.Inserted {
			continue
		}
		if _, err := c.Post(ctx, device.URI()+"/Actions/VirtualMedia.EjectMedia", map[string]any{}); err != nil {
			return fmt.Errorf("Virtual Media 제거에 실패했습니다: %s: %w", device.ID, err)
		}
		ejected++
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}

	remaining := 0
	for _, device := range devices {
		var current VirtualMedia
		if err := c.Get(ctx, device.URI(), &current); err != nil {
			return fmt.Errorf("Virtual Media 상태를 확인하지 못했습니다: %s: %w", device.URI(), err)
		}
		logx.Info("장치 %s: Inserted=%t, ConnectedVia=%s, ImageName=%s",
			device.ID, current.Inserted, orDash(current.ConnectedVia), orDash(current.ImageName))
		if current.Inserted {
			remaining++
		}
	}
	if remaining != 0 {
		return fmt.Errorf("제거되지 않은 Virtual Media가 %d개 있습니다", remaining)
	}
	logx.Info("Virtual Media 제거 완료: %d개. 모든 장치가 Inserted=false 상태입니다.", ejected)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// removeVirtualMediaFromBootOrder는 평상시 부트 순서에서 가상 CD/DVD를 제외합니다.
//
// UEFI BootOrder에 Virtual Optical Drive가 남아 있으면, 앞순위 항목이
// Unavailable이 되는 순간 펌웨어가 마운트된 ISO로 다시 부팅해 무한 재부팅이
// 발생합니다. 설치 시에는 Dell 전용 FirstBootDevice로 한 번만 강제하므로
// 부트 순서에 VCD가 없어도 이번 부팅은 정상적으로 ISO에서 시작됩니다.
//
// 이 단계는 재부팅 반복을 줄이기 위한 보호 장치이므로 실패해도 설치를
// 중단하지 않습니다. 부트 항목을 읽지 못하면 해당 항목을 유지하고, PATCH가
// 거부되면 경고를 남긴 뒤 원타임 부트 설정을 계속합니다. 장비와 펌웨어에
// 따라 BootOrder 표현과 쓰기 지원이 다를 수 있기 때문입니다. 설치 후에는
// Eject와 FirstBootDevice=Normal 확인으로 재부팅 반복을 방지합니다.
func removeVirtualMediaFromBootOrder(ctx context.Context, c *redfish.Client) error {
	sys, err := GetSystem(ctx, c)
	if err != nil {
		// 이 함수는 보호 기능이므로 어느 단계가 실패해도 설치 작업을
		// 막지 않습니다(fail-open). 유일한 오류는 컨텍스트 취소입니다.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logx.Warn("부트 순서를 조회하지 못해 가상 미디어 제외를 건너뜁니다. "+
			"설치 후 부트 순서를 직접 확인하세요: %v", err)
		return nil
	}
	order := sys.Boot.BootOrder
	if len(order) == 0 {
		logx.Debug("부트 순서 정보가 없어 변경하지 않습니다.")
		return nil
	}

	var keep, drop []string
	for _, ref := range order {
		var option BootOption
		if err := c.Get(ctx, SystemPath+"/BootOptions/"+ref, &option); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// 개별 부트 항목 조회 실패는 치명적이지 않지만, 그 항목이
			// 가상 미디어인지 확인하지 못한 채 유지되므로 보호가
			// 불완전할 수 있음을 운영자에게 알립니다.
			logx.Warn("부트 항목을 조회하지 못해 그대로 유지합니다: %s: %v", ref, err)
			keep = append(keep, ref)
			continue
		}
		if isVirtualMediaBootOption(option) {
			logx.Info("  %s : %s  <- 가상 미디어 (부트 순서에서 제외)", ref, option.DisplayName)
			drop = append(drop, ref)
			continue
		}
		logx.Debug("  %s : %s", ref, option.DisplayName)
		keep = append(keep, ref)
	}

	switch {
	case len(drop) == 0:
		logx.Info("부트 순서에 가상 미디어 항목이 없습니다. 변경하지 않습니다.")
		return nil
	case len(keep) == 0:
		logx.Warn("가상 미디어를 빼면 부트 순서가 비게 됩니다. 변경하지 않습니다.")
		return nil
	}

	// BootOrder는 Systems 리소스에 직접 PATCH할 수 없고 Settings에만 쓸 수 있습니다.
	// 변경은 pending 설정으로 등록되어 다음 부팅에서 적용됩니다.
	if _, err := c.Patch(ctx, SystemSettingsPath, map[string]any{
		"Boot": map[string]any{"BootOrder": keep},
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logx.Warn("부트 순서 변경 요청이 거부되었습니다. 원타임 부트 설정으로 계속 진행합니다: %v", err)
		return nil
	}
	logx.Info("부트 순서 변경이 pending 설정으로 등록되었습니다. 다음 부팅에서 적용됩니다.")
	return nil
}
