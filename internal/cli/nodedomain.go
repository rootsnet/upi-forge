package cli

import (
	"errors"
	"fmt"

	"upi-forge/internal/csvdata"
	"upi-forge/internal/dnscheck"
)

// checkNodeDomains는 nodes.csv의 모든 FQDN hostname이 클러스터 도메인
// 소속인지 확인합니다.
//
// ignition뿐 아니라 iso와 boot에서도 실행합니다.
// ignition에만 있으면 검사 추가 이전에 만들어진 잘못된 산출물
// (다른 클러스터 이름의 <hostname>.ign/<hostname>.iso)을 iso와 boot가
// 계속 소비할 수 있기 때문입니다. inventory/live-boot/eject는 설치
// 산출물을 다루지 않는 조사·정리 단계라 적용하지 않습니다.
// 오류에는 어떤 pathset의 어떤 파일을 읽었는지 함께 표시합니다.
// 여러 pathset을 오가는 환경에서 잘못된 pathset이 활성화된 것인지,
// CSV 내용이 잘못된 것인지 바로 구분하기 위해서입니다.
func checkNodeDomains(pathsetName string, nodes *csvdata.Nodes, domain string) error {
	var problems []error
	for _, node := range nodes.Items {
		if err := dnscheck.CheckHostnameDomain(node.Hostname, domain); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("노드 hostname 검증에 실패했습니다 (%d건):\n선택 pathset: %s\n읽은 파일: %s\n%w",
			len(problems), pathsetName, nodes.Path, errors.Join(problems...))
	}
	return nil
}
