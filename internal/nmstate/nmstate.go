// Package nmstate는 nodes.csv, nics.csv와 pathset 설정을 사용해 노드별
// NMState YAML을 생성합니다. 출력 형식은 기존 산출물과 바이트 단위로
// 호환되어, 내용이 같은 YAML을 안전하게 재사용할 수 있습니다.
package nmstate

import (
	"fmt"
	"strings"

	"upi-forge/internal/config"
)

// Input은 한 노드의 NMState YAML을 만드는 데 필요한 값입니다.
type Input struct {
	// NICs는 nics.csv의 전체 물리 NIC 이름입니다. 파일 순서를 유지합니다.
	NICs []string
	// IP는 이 노드에 설정할 IPv4 주소입니다.
	IP string
	// Network는 pathset의 네트워크 설정입니다.
	Network config.Network
}

// Generate는 한 노드의 NMState YAML 본문을 만듭니다.
//
// 생성 규칙:
//   - nics.csv의 모든 물리 NIC를 출력합니다. 사용하지 않는 NIC는 IPv4/IPv6를 끕니다.
//   - bond를 쓰지 않으면 activeNIC에만 IP를 붙입니다.
//   - bond를 쓰면 물리 NIC는 모두 IP를 끄고 bond 인터페이스에 IP를 붙입니다.
//   - 기본 경로는 bond 사용 여부에 따라 bond 또는 activeNIC을 next-hop으로 씁니다.
//   - DNS는 설정한 순서대로 나열하고, 비어 있으면 dns-resolver 블록을 생략합니다.
func Generate(in Input) ([]byte, error) {
	if in.IP == "" {
		return nil, fmt.Errorf("노드 IP가 비어 있습니다")
	}
	if len(in.NICs) == 0 {
		return nil, fmt.Errorf("nics.csv에서 읽은 NIC 목록이 비어 있습니다")
	}
	net := in.Network

	var b strings.Builder
	b.WriteString("interfaces:\n")

	for _, nic := range in.NICs {
		if !net.Bonding && nic == net.ActiveNIC {
			writeEthernetWithIP(&b, nic, in.IP, net.PrefixLength)
			continue
		}
		writeEthernetDisabled(&b, nic)
	}

	routeInterface := net.ActiveNIC
	if net.Bonding {
		writeBond(&b, net, in.IP)
		routeInterface = net.Bond.Name
	}

	fmt.Fprintf(&b, `routes:
  config:
    - destination: 0.0.0.0/0
      next-hop-address: %s
      next-hop-interface: %s
      table-id: 254
`, net.Gateway, routeInterface)

	if len(net.DNS) > 0 {
		b.WriteString("dns-resolver:\n  config:\n    server:\n")
		for _, server := range net.DNS {
			fmt.Fprintf(&b, "      - %s\n", server)
		}
	}
	return []byte(b.String()), nil
}

func writeEthernetWithIP(b *strings.Builder, nic, ip string, prefix int) {
	fmt.Fprintf(b, `  - name: %s
    type: ethernet
    state: up
    ipv4:
      enabled: true
      dhcp: false
      address:
        - ip: %s
          prefix-length: %d
    ipv6:
      enabled: false
`, nic, ip, prefix)
}

func writeEthernetDisabled(b *strings.Builder, nic string) {
	fmt.Fprintf(b, `  - name: %s
    type: ethernet
    state: up
    ipv4:
      enabled: false
    ipv6:
      enabled: false
`, nic)
}

func writeBond(b *strings.Builder, net config.Network, ip string) {
	fmt.Fprintf(b, `  - name: %s
    type: bond
    state: up
    link-aggregation:
      mode: %s
      options:
        miimon: "%d"
        primary: %s
      port:
        - %s
        - %s
    ipv4:
      enabled: true
      dhcp: false
      address:
        - ip: %s
          prefix-length: %d
    ipv6:
      enabled: false
`, net.Bond.Name, net.Bond.Mode, net.Bond.MIIMon, net.Bond.Primary,
		net.Bond.Primary, net.Bond.Standby, ip, net.PrefixLength)
}
