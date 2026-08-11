package idrac

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"upi-forge/internal/bmc"
	"upi-forge/internal/logx"
	"upi-forge/internal/redfish"
)

// --- Redfish 응답 타입 ------------------------------------------------------

type pcieDevice struct {
	Links struct {
		PCIeFunctions []redfish.Ref `json:"PCIeFunctions"`
	} `json:"Links"`
	PCIeFunctions redfish.Ref `json:"PCIeFunctions"`
}

type pcieFunctionResource struct {
	ID          string `json:"Id"`
	Name        string `json:"Name"`
	DeviceClass string `json:"DeviceClass"`
	Oem         struct {
		Dell struct {
			DellPCIeFunction struct {
				SlotType string `json:"SlotType"`
				ID       string `json:"Id"`
			} `json:"DellPCIeFunction"`
		} `json:"Dell"`
	} `json:"Oem"`
	Links struct {
		StorageControllers []redfish.Ref `json:"StorageControllers"`
	} `json:"Links"`
}

type storageResource struct {
	ID                string        `json:"Id"`
	Name              string        `json:"Name"`
	Drives            []redfish.Ref `json:"Drives"`
	Volumes           redfish.Ref   `json:"Volumes"`
	StorageController []struct {
		Model           string `json:"Model"`
		FirmwareVersion string `json:"FirmwareVersion"`
	} `json:"StorageControllers"`
	Status redfish.Status `json:"Status"`
}

type driveResource struct {
	ID            string `json:"Id"`
	Name          string `json:"Name"`
	Model         string `json:"Model"`
	Manufacturer  string `json:"Manufacturer"`
	SerialNumber  string `json:"SerialNumber"`
	MediaType     string `json:"MediaType"`
	Protocol      string `json:"Protocol"`
	CapacityBytes *int64 `json:"CapacityBytes"`
	Identifiers   []struct {
		DurableName       string `json:"DurableName"`
		DurableNameFormat string `json:"DurableNameFormat"`
	} `json:"Identifiers"`
	PhysicalLocation struct {
		PartLocation struct {
			LocationOrdinalValue *int `json:"LocationOrdinalValue"`
		} `json:"PartLocation"`
	} `json:"PhysicalLocation"`
	PredictedMediaLifeLeftPercent *int           `json:"PredictedMediaLifeLeftPercent"`
	Status                        redfish.Status `json:"Status"`
}

type volumeResource struct {
	ID            string `json:"Id"`
	Name          string `json:"Name"`
	DisplayName   string `json:"DisplayName"`
	RAIDType      string `json:"RAIDType"`
	VolumeType    string `json:"VolumeType"`
	CapacityBytes *int64 `json:"CapacityBytes"`
	Links         struct {
		Drives []redfish.Ref `json:"Drives"`
	} `json:"Links"`
	Status redfish.Status `json:"Status"`
}

// CollectStorageInventory는 스토리지 인벤토리를 수집합니다.
// 조회(GET)만 수행하며 서버 전원, BIOS, RAID 구성을 변경하지 않습니다.
//
// 목적은 coreos-installer의 dest-device 값을 정하기 위한 근거 수집입니다.
// PCI 주소까지는 확정할 수 있지만, /dev/disk/by-path의 접미사(-nvme-1 등)는
// 커널이 붙이므로 최종 확인은 라이브 ISO 부팅 후 ls -l /dev/disk/by-path 로 해야 합니다.
func CollectStorageInventory(ctx context.Context, c *redfish.Client) (*bmc.StorageInventory, error) {
	logx.Info("[1/3] 시스템 식별 정보 조회")
	sys, err := GetSystem(ctx, c)
	if err != nil {
		return nil, err
	}
	serviceTag := sys.SKU
	if serviceTag == "" {
		serviceTag = sys.SerialNumber
	}

	inv := &bmc.StorageInventory{
		SchemaVersion:        bmc.StorageInventorySchemaVersion,
		CollectedAt:          time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		BMCAddress:           c.Host(),
		SystemModel:          sys.Model,
		ServiceTag:           serviceTag,
		StoragePcieFunctions: []bmc.PCIeFunction{},
		Storage:              []bmc.StorageContainer{},
	}
	logx.Info("      Model=%s, ServiceTag=%s, BiosVersion=%s, PowerState=%s",
		sys.Model, serviceTag, sys.BiosVersion, sys.PowerState)

	logx.Info("[2/3] PCIe 스토리지 장치 조회")
	functions, err := collectStoragePCIeFunctions(ctx, c)
	if err != nil {
		return nil, err
	}
	inv.StoragePcieFunctions = functions

	logx.Info("[3/3] 스토리지 컨트롤러와 디스크 조회")
	containers, err := collectStorageControllers(ctx, c, functions)
	if err != nil {
		return nil, err
	}
	inv.Storage = containers
	return inv, nil
}

func collectStoragePCIeFunctions(ctx context.Context, c *redfish.Client) ([]bmc.PCIeFunction, error) {
	var devices redfish.Collection
	if err := c.Get(ctx, ChassisPath+"/PCIeDevices", &devices); err != nil {
		return nil, err
	}
	uris := devices.IDs()
	sort.Strings(uris)

	out := []bmc.PCIeFunction{}
	for _, deviceURI := range uris {
		var device pcieDevice
		if err := c.Get(ctx, deviceURI, &device); err != nil {
			logx.Debug("PCIe 장치를 조회하지 못했습니다(건너뜀): %s: %v", deviceURI, err)
			continue
		}

		// PCIeFunctions는 Links 배열 또는 컬렉션 링크로 올 수 있습니다.
		funcURIs := make([]string, 0, len(device.Links.PCIeFunctions))
		for _, ref := range device.Links.PCIeFunctions {
			if ref.ID != "" {
				funcURIs = append(funcURIs, ref.ID)
			}
		}
		if len(funcURIs) == 0 && device.PCIeFunctions.ID != "" {
			var collection redfish.Collection
			if err := c.Get(ctx, device.PCIeFunctions.ID, &collection); err == nil {
				funcURIs = collection.IDs()
			}
		}

		for _, funcURI := range funcURIs {
			var fn pcieFunctionResource
			if err := c.Get(ctx, funcURI, &fn); err != nil {
				logx.Debug("PCIe 함수를 조회하지 못했습니다(건너뜀): %s: %v", funcURI, err)
				continue
			}
			if fn.ID == "" {
				continue
			}
			// 스토리지와 무관한 장치(NIC, GPU, USB 등)는 건너뜁니다.
			switch fn.DeviceClass {
			case "MassStorageController", "NonVolatileMemoryController":
			default:
				continue
			}

			addr := pciAddressFromFunctionID(fn.ID)
			links := make([]string, 0, len(fn.Links.StorageControllers))
			for _, ref := range fn.Links.StorageControllers {
				links = append(links, ref.ID)
			}
			out = append(out, bmc.PCIeFunction{
				FunctionID:             fn.ID,
				PciAddress:             addr,
				PredictedByPathPrefix:  byPathPrefix(addr),
				DeviceClass:            fn.DeviceClass,
				Name:                   fn.Name,
				SlotType:               nilIfEmpty(fn.Oem.Dell.DellPCIeFunction.SlotType),
				ControllerID:           nilIfEmpty(fn.Oem.Dell.DellPCIeFunction.ID),
				StorageControllerLinks: links,
				PCIeDeviceURI:          deviceURI,
			})
		}
	}
	return out, nil
}

func collectStorageControllers(ctx context.Context, c *redfish.Client,
	functions []bmc.PCIeFunction) ([]bmc.StorageContainer, error) {

	var storages redfish.Collection
	if err := c.Get(ctx, SystemPath+"/Storage", &storages); err != nil {
		return nil, err
	}
	uris := storages.IDs()
	sort.Strings(uris)

	out := []bmc.StorageContainer{}
	for _, storageURI := range uris {
		var resource storageResource
		if err := c.Get(ctx, storageURI, &resource); err != nil {
			return nil, err
		}

		container := bmc.StorageContainer{
			ID:      resource.ID,
			Drives:  []bmc.Drive{},
			Volumes: []bmc.Volume{},
			Controller: bmc.Controller{
				Name:  resource.Name,
				State: nilIfEmpty(resource.Status.State),
			},
		}
		if len(resource.StorageController) > 0 {
			container.Controller.Model = nilIfEmpty(resource.StorageController[0].Model)
			container.Controller.FirmwareVersion = nilIfEmpty(resource.StorageController[0].FirmwareVersion)
		}
		container.Pcie = matchPCIeFunction(functions, resource.ID, storageURI)

		driveURIs := make([]string, 0, len(resource.Drives))
		for _, ref := range resource.Drives {
			if ref.ID != "" {
				driveURIs = append(driveURIs, ref.ID)
			}
		}
		sort.Strings(driveURIs)
		for _, driveURI := range driveURIs {
			var drive driveResource
			if err := c.Get(ctx, driveURI, &drive); err != nil {
				return nil, err
			}
			container.Drives = append(container.Drives, toDrive(drive))
		}

		if resource.Volumes.ID != "" {
			var volumes redfish.Collection
			if err := c.Get(ctx, resource.Volumes.ID, &volumes); err != nil {
				return nil, err
			}
			volumeURIs := volumes.IDs()
			sort.Strings(volumeURIs)
			for _, volumeURI := range volumeURIs {
				var volume volumeResource
				if err := c.Get(ctx, volumeURI, &volume); err != nil {
					return nil, err
				}
				container.Volumes = append(container.Volumes, toVolume(volume))
			}
		}
		out = append(out, container)
	}
	return out, nil
}

// matchPCIeFunction은 컨트롤러에 대응하는 PCIe 함수를 찾습니다.
//  1. Oem.Dell.DellPCIeFunction.Id가 컨트롤러 Id와 같은 경우 (예: BOSS.Slot.3-1)
//  2. Links.StorageControllers가 이 컨트롤러 URI를 가리키는 경우
func matchPCIeFunction(functions []bmc.PCIeFunction, controllerID, controllerURI string) *bmc.PCIeFunction {
	for i := range functions {
		fn := &functions[i]
		if fn.ControllerID != nil && *fn.ControllerID == controllerID {
			return fn
		}
		for _, link := range fn.StorageControllerLinks {
			// 링크는 컨트롤러 URI 뒤에 "#..." 조각이 붙을 수 있어 접두사로
			// 비교하되, 경로 경계를 구분해 ".../CPU.1"이 ".../CPU.10"의
			// 링크와 잘못 짝지어지지 않게 합니다.
			if link == controllerURI ||
				strings.HasPrefix(link, controllerURI+"#") ||
				strings.HasPrefix(link, controllerURI+"/") {
				return fn
			}
		}
	}
	return nil
}

// byPathPrefix는 PCI 주소로 by-path 예상 접두사를 만듭니다.
// 주소를 해석하지 못했으면 빈 문자열을 유지해, "pci-"까지만 붙은 절반의
// 경로가 예상값처럼 보이지 않게 합니다.
func byPathPrefix(addr string) string {
	if addr == "" {
		return ""
	}
	return "/dev/disk/by-path/pci-" + addr
}

func toDrive(d driveResource) bmc.Drive {
	drive := bmc.Drive{
		ID:              d.ID,
		Name:            d.Name,
		Model:           d.Model,
		Manufacturer:    d.Manufacturer,
		SerialNumber:    d.SerialNumber,
		MediaType:       d.MediaType,
		Protocol:        d.Protocol,
		CapacityBytes:   d.CapacityBytes,
		CapacityGB:      capacityGB(d.CapacityBytes),
		CapacityGiB:     capacityGiB(d.CapacityBytes),
		SlotOrdinal:     d.PhysicalLocation.PartLocation.LocationOrdinalValue,
		LifeLeftPercent: d.PredictedMediaLifeLeftPercent,
		State:           nilIfEmpty(d.Status.State),
	}
	if len(d.Identifiers) > 0 {
		drive.DurableName = nilIfEmpty(d.Identifiers[0].DurableName)
		drive.DurableNameFormat = nilIfEmpty(d.Identifiers[0].DurableNameFormat)
	}
	return drive
}

func toVolume(v volumeResource) bmc.Volume {
	name := v.DisplayName
	if name == "" {
		name = v.Name
	}
	drives := make([]string, 0, len(v.Links.Drives))
	for _, ref := range v.Links.Drives {
		drives = append(drives, ref.ID)
	}
	return bmc.Volume{
		ID:            v.ID,
		Name:          strings.TrimRight(name, " \t"),
		RAIDType:      v.RAIDType,
		VolumeType:    v.VolumeType,
		CapacityBytes: v.CapacityBytes,
		CapacityGB:    capacityGB(v.CapacityBytes),
		CapacityGiB:   capacityGiB(v.CapacityBytes),
		MemberDrives:  drives,
		State:         nilIfEmpty(v.Status.State),
	}
}

// pciAddressFromFunctionID는 Dell PCIeFunction Id를 PCI 주소로 바꿉니다.
//
// Dell의 PCIeFunction Id는 "<segment>-<bus>-<device>-<function>" 형식이고
// 값은 10진수입니다.
//
//	"0-60-0-0" -> segment 0, bus 60(0x3c), device 0, function 0
//	           -> 0000:3c:00.0
//	           -> /dev/disk/by-path/pci-0000:3c:00.0-nvme-1 (예상)
func pciAddressFromFunctionID(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) != 4 {
		return ""
	}
	values := make([]int, 4)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return ""
		}
		values[i] = n
	}
	return fmt.Sprintf("%04x:%02x:%02x.%x", values[0], values[1], values[2], values[3])
}

func capacityGB(bytes *int64) *float64 {
	if bytes == nil {
		return nil
	}
	v := round1(float64(*bytes) / 1_000_000_000)
	return &v
}

func capacityGiB(bytes *int64) *float64 {
	if bytes == nil {
		return nil
	}
	v := round1(float64(*bytes) / 1_073_741_824)
	return &v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
