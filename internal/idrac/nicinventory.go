package idrac

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

// serviceRoot는 Redfish 서비스 루트에서 필요한 필드입니다.
type serviceRoot struct {
	Systems               redfish.Ref `json:"Systems"`
	Chassis               redfish.Ref `json:"Chassis"`
	ServiceIdentification string      `json:"ServiceIdentification"`
	Oem                   struct {
		Dell struct {
			ServiceTag string `json:"ServiceTag"`
		} `json:"Dell"`
	} `json:"Oem"`
}

type ethernetInterface struct {
	ID                  string         `json:"Id"`
	PermanentMACAddress string         `json:"PermanentMACAddress"`
	MACAddress          string         `json:"MACAddress"`
	LinkStatus          string         `json:"LinkStatus"`
	SpeedMbps           *int           `json:"SpeedMbps"`
	Status              redfish.Status `json:"Status"`
}

type networkAdapter struct {
	ID           string `json:"Id"`
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
	PartNumber   string `json:"PartNumber"`
	SerialNumber string `json:"SerialNumber"`
	Controllers  []struct {
		FirmwarePackageVersion string `json:"FirmwarePackageVersion"`
	} `json:"Controllers"`
	Status                 redfish.Status `json:"Status"`
	NetworkDeviceFunctions redfish.Ref    `json:"NetworkDeviceFunctions"`
}

type networkDeviceFunction struct {
	ID       string `json:"Id"`
	Ethernet struct {
		PermanentMACAddress string `json:"PermanentMACAddress"`
		MACAddress          string `json:"MACAddress"`
	} `json:"Ethernet"`
	Status redfish.Status `json:"Status"`
	Links  struct {
		PhysicalPortAssignment        redfish.Ref `json:"PhysicalPortAssignment"`
		PhysicalNetworkPortAssignment redfish.Ref `json:"PhysicalNetworkPortAssignment"`
	} `json:"Links"`
}

type networkPort struct {
	ID                   string         `json:"Id"`
	LinkStatus           string         `json:"LinkStatus"`
	CurrentLinkSpeedMbps *int           `json:"CurrentLinkSpeedMbps"`
	Status               redfish.Status `json:"Status"`
}

type chassisResource struct {
	NetworkAdapters redfish.Ref `json:"NetworkAdapters"`
}

// CollectNICInventory는 NIC 인벤토리를 수집합니다. 조회(GET)만 수행하며
// 서버 전원, BIOS, NIC 설정을 변경하지 않습니다.
//
// 탐색 경로:
//
//	Service Root -> Systems -> System -> EthernetInterfaces (보충 정보)
//	             -> Chassis -> 각 Chassis -> NetworkAdapters
//	             -> 각 Adapter -> NetworkDeviceFunctions -> PhysicalPortAssignment
//
// 컬렉션은 Members[].@odata.id를 모두 따라가므로 NIC 카드/포트 수를
// 하드코딩하지 않습니다. 서버 모델이 달라져도 그대로 사용할 수 있습니다.
func CollectNICInventory(ctx context.Context, c *redfish.Client) (*bmc.NICInventory, error) {
	logx.Info("[1/5] Service Root와 Systems 탐색")
	var root serviceRoot
	if err := c.Get(ctx, "/redfish/v1", &root); err != nil {
		return nil, err
	}
	if root.Systems.ID == "" || root.Chassis.ID == "" {
		return nil, fmt.Errorf("Service Root에서 Systems 또는 Chassis 링크를 찾지 못했습니다")
	}

	var systems redfish.Collection
	if err := c.Get(ctx, root.Systems.ID, &systems); err != nil {
		return nil, err
	}
	if err := systems.Check("Systems"); err != nil {
		return nil, err
	}
	if len(systems.Members) != 1 {
		return nil, fmt.Errorf("System이 정확히 하나여야 합니다: 발견=%d", len(systems.Members))
	}

	var sys System
	if err := c.Get(ctx, systems.Members[0].ID, &sys); err != nil {
		return nil, err
	}

	serviceTag := root.ServiceIdentification
	if serviceTag == "" {
		serviceTag = root.Oem.Dell.ServiceTag
	}
	if serviceTag == "" {
		serviceTag = sys.SerialNumber
	}

	inv := &bmc.NICInventory{
		SchemaVersion:  bmc.NICInventorySchemaVersion,
		Purpose:        "ocp-install-nic-inventory",
		CollectedAtUTC: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		System: bmc.NICSystem{
			ID:           sys.ID,
			Manufacturer: sys.Manufacturer,
			Model:        sys.Model,
			ServiceTag:   serviceTag,
			SerialNumber: sys.SerialNumber,
			UUID:         sys.UUID,
		},
	}

	logx.Info("[2/5] EthernetInterfaces 보충 정보 수집")
	fallback, err := collectEthernetInterfaces(ctx, c, sys.EthernetInterfaces.ID)
	if err != nil {
		return nil, err
	}

	logx.Info("[3/5] Chassis에서 NetworkAdapters 탐색")
	adaptersURI, err := findNetworkAdaptersCollection(ctx, c, root.Chassis.ID)
	if err != nil {
		return nil, err
	}
	var adapters redfish.Collection
	if err := c.Get(ctx, adaptersURI, &adapters); err != nil {
		return nil, err
	}
	if err := adapters.Check("Network Adapter"); err != nil {
		return nil, err
	}
	if len(adapters.Members) == 0 {
		return nil, fmt.Errorf("NIC 어댑터를 찾지 못했습니다")
	}

	logx.Info("[4/5] Adapter -> NetworkDeviceFunctions -> Physical Port 탐색")
	type adapterEntry struct {
		uri  string
		data networkAdapter
	}
	entries := make([]adapterEntry, 0, len(adapters.Members))
	for _, member := range adapters.Members {
		var data networkAdapter
		if err := c.Get(ctx, member.ID, &data); err != nil {
			return nil, err
		}
		entries = append(entries, adapterEntry{uri: member.ID, data: data})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return naturalLess(entries[i].data.ID, entries[j].data.ID)
	})

	for _, entry := range entries {
		adapter, err := buildAdapter(ctx, c, entry.data, fallback)
		if err != nil {
			return nil, err
		}
		inv.Adapters = append(inv.Adapters, adapter)
	}

	logx.Info("[5/5] 필수 MAC 주소 검증")
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	return inv, nil
}

func collectEthernetInterfaces(ctx context.Context, c *redfish.Client, uri string) (map[string]ethernetInterface, error) {
	out := map[string]ethernetInterface{}
	if uri == "" {
		return out, nil
	}
	var collection redfish.Collection
	if err := c.Get(ctx, uri, &collection); err != nil {
		return nil, err
	}
	for _, member := range collection.Members {
		var iface ethernetInterface
		if err := c.Get(ctx, member.ID, &iface); err != nil {
			return nil, err
		}
		if iface.ID != "" {
			out[iface.ID] = iface
		}
	}
	return out, nil
}

func findNetworkAdaptersCollection(ctx context.Context, c *redfish.Client, chassisURI string) (string, error) {
	var chassisCollection redfish.Collection
	if err := c.Get(ctx, chassisURI, &chassisCollection); err != nil {
		return "", err
	}
	if err := chassisCollection.Check("Chassis"); err != nil {
		return "", err
	}
	unique := map[string]struct{}{}
	for _, member := range chassisCollection.Members {
		var chassis chassisResource
		if err := c.Get(ctx, member.ID, &chassis); err != nil {
			return "", err
		}
		if chassis.NetworkAdapters.ID != "" {
			unique[chassis.NetworkAdapters.ID] = struct{}{}
		}
	}
	if len(unique) != 1 {
		return "", fmt.Errorf("NetworkAdapters 컬렉션이 %d개입니다. 하나여야 합니다", len(unique))
	}
	for uri := range unique {
		return uri, nil
	}
	return "", fmt.Errorf("NetworkAdapters 컬렉션을 찾지 못했습니다")
}

func buildAdapter(ctx context.Context, c *redfish.Client, data networkAdapter,
	fallback map[string]ethernetInterface) (bmc.NICAdapter, error) {

	adapter := bmc.NICAdapter{
		ID:           data.ID,
		Slot:         parseIntPtr(strings.TrimPrefix(data.ID, "NIC.Slot.")),
		Manufacturer: data.Manufacturer,
		Model:        data.Model,
		PartNumber:   data.PartNumber,
		SerialNumber: data.SerialNumber,
		Health:       data.Status.Health,
		Ports:        []bmc.NICPort{},
	}
	if len(data.Controllers) > 0 {
		adapter.Firmware = data.Controllers[0].FirmwarePackageVersion
	}
	if data.NetworkDeviceFunctions.ID == "" {
		return adapter, nil
	}

	var functions redfish.Collection
	if err := c.Get(ctx, data.NetworkDeviceFunctions.ID, &functions); err != nil {
		return adapter, err
	}
	if err := functions.Check(data.ID + " NetworkDeviceFunctions"); err != nil {
		return adapter, err
	}

	list := make([]networkDeviceFunction, 0, len(functions.Members))
	for _, member := range functions.Members {
		var ndf networkDeviceFunction
		if err := c.Get(ctx, member.ID, &ndf); err != nil {
			return adapter, err
		}
		list = append(list, ndf)
	}
	sort.SliceStable(list, func(i, j int) bool { return naturalLess(list[i].ID, list[j].ID) })

	for _, ndf := range list {
		port, err := buildPort(ctx, c, data.ID, ndf, fallback)
		if err != nil {
			return adapter, err
		}
		adapter.Ports = append(adapter.Ports, port)
	}
	return adapter, nil
}

func buildPort(ctx context.Context, c *redfish.Client, adapterID string,
	ndf networkDeviceFunction, fallback map[string]ethernetInterface) (bmc.NICPort, error) {

	// Dell NetworkDeviceFunction의 영구 MAC을 우선 사용합니다.
	// 펌웨어나 NIC가 영구 필드를 비우면 현재 MAC, 마지막으로 EthernetInterfaces로 폴백합니다.
	permanent := strings.ToUpper(ndf.Ethernet.PermanentMACAddress)
	if permanent == "" {
		permanent = strings.ToUpper(ndf.Ethernet.MACAddress)
	}
	current := strings.ToUpper(ndf.Ethernet.MACAddress)

	// function id는 "<adapterId>-<물리포트>-<파티션>" 형식입니다.
	suffix := strings.TrimPrefix(ndf.ID, adapterID+"-")
	parts := strings.Split(suffix, "-")
	var physical, partition *int
	if len(parts) > 0 {
		physical = parseIntPtr(parts[0])
	}
	if len(parts) > 1 {
		partition = parseIntPtr(parts[1])
	}

	portID := adapterID + "-" + parts[0]
	link, health := "", ""
	var speed *int

	portURI := ndf.Links.PhysicalPortAssignment.ID
	if portURI == "" {
		portURI = ndf.Links.PhysicalNetworkPortAssignment.ID
	}
	if portURI != "" {
		var port networkPort
		if err := c.Get(ctx, portURI, &port); err != nil {
			if ctx.Err() != nil {
				return bmc.NICPort{}, ctx.Err()
			}
			// BMC가 광고한 포트 링크가 실제로는 없을 수 있습니다.
			// 일부 카드는 물리 포트의 장치 기능은 노출하면서 NetworkPorts
			// 컬렉션에는 해당 포트 리소스를 만들지 않아 링크가 404가
			// 됩니다. 참고 정보 수집이므로 이 경우만 경고 후 장치 기능과
			// EthernetInterfaces 값으로 계속하고, 인증·권한·서버 오류 등
			// 다른 실패는 실제 문제이므로 그대로 중단합니다.
			var apiErr *redfish.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
				return bmc.NICPort{}, err
			}
			logx.Warn("%s: BMC가 광고한 포트 리소스가 없습니다(HTTP 404). 장치 기능 값으로 계속합니다: %v",
				ndf.ID, err)
		} else {
			if port.ID != "" {
				portID = port.ID
			}
			link = port.LinkStatus
			speed = port.CurrentLinkSpeedMbps
			health = port.Status.Health
		}
	}

	if iface, ok := fallback[ndf.ID]; ok {
		if permanent == "" {
			permanent = strings.ToUpper(iface.PermanentMACAddress)
		}
		if current == "" {
			current = strings.ToUpper(iface.MACAddress)
		}
		if link == "" {
			link = iface.LinkStatus
		}
		if speed == nil {
			speed = iface.SpeedMbps
		}
		if health == "" {
			health = iface.Status.Health
		}
	}
	if health == "" {
		health = ndf.Status.Health
	}

	return bmc.NICPort{
		ID:                  portID,
		FunctionID:          ndf.ID,
		PhysicalPort:        physical,
		Partition:           partition,
		PermanentMACAddress: permanent,
		CurrentMACAddress:   current,
		LinkStatus:          normalizeLinkStatus(link),
		CurrentSpeedMbps:    speed,
		Health:              health,
	}, nil
}

// normalizeLinkStatus는 펌웨어마다 다른 링크 상태 표기를 통일합니다.
func normalizeLinkStatus(s string) string {
	switch s {
	case "Up", "LinkUp", "Connected":
		return "Up"
	case "Down", "LinkDown", "Disconnected":
		return "Down"
	case "":
		return "Unknown"
	default:
		return s
	}
}

func parseIntPtr(s string) *int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &n
}

// naturalLess는 "NIC.Slot.2"가 "NIC.Slot.10"보다 앞에 오도록 비교합니다.
func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		if isDigit(a[ai]) && isDigit(b[bi]) {
			aj, bj := ai, bi
			for aj < len(a) && isDigit(a[aj]) {
				aj++
			}
			for bj < len(b) && isDigit(b[bj]) {
				bj++
			}
			an, _ := strconv.Atoi(a[ai:aj])
			bn, _ := strconv.Atoi(b[bi:bj])
			if an != bn {
				return an < bn
			}
			ai, bi = aj, bj
			continue
		}
		if a[ai] != b[bi] {
			return a[ai] < b[bi]
		}
		ai++
		bi++
	}
	return len(a)-ai < len(b)-bi
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
