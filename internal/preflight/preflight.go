// Package preflight는 NMState YAML이나 Ignition을 만들기 전에 OpenShift
// 로그인 상태와 대상 클러스터를 확인합니다.
//
//	oc whoami                 로그인 여부
//	oc whoami --show-server   설정의 대상 클러스터와 일치하는지
//	oc get --raw=/readyz      API 접근 가능 여부
//
// 하나라도 실패하면 산출물을 만들지 않고 중단하여, 현재 로그인한 클러스터와
// 설정의 대상 클러스터가 다른 상태에서 Ignition이 생성되는 것을 막습니다.
package preflight

import (
	"context"
	"fmt"
	"strings"
	"time"

	"upi-forge/internal/execx"
	"upi-forge/internal/logx"
)

// Result는 사전 점검 결과입니다.
type Result struct {
	User   string
	Server string
}

// Check는 oc 로그인 상태와 대상 클러스터 일치 여부를 확인합니다.
// expectedServer는 설정의 cluster.apiURL입니다.
func Check(ctx context.Context, ocPath, expectedServer string) (Result, error) {
	expected := strings.TrimRight(strings.TrimSpace(expectedServer), "/")
	if expected == "" {
		return Result{}, fmt.Errorf("설정의 cluster.apiURL이 비어 있어 대상 클러스터를 확인할 수 없습니다")
	}
	if _, err := execx.Require(ocPath); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	user, err := execx.Output(ctx, ocPath, "whoami")
	if err != nil || strings.TrimSpace(user) == "" {
		return Result{}, fmt.Errorf("OpenShift에 로그인되어 있지 않거나 현재 인증 정보가 유효하지 않습니다. 먼저 oc login을 실행하세요")
	}
	user = strings.TrimSpace(user)

	server, err := execx.Output(ctx, ocPath, "whoami", "--show-server")
	if err != nil || strings.TrimSpace(server) == "" {
		return Result{}, fmt.Errorf("현재 oc 로그인 대상 API 주소를 확인할 수 없습니다")
	}
	server = strings.TrimRight(strings.TrimSpace(server), "/")

	if server != expected {
		return Result{}, fmt.Errorf(
			"설정의 대상 클러스터와 현재 oc 로그인 대상이 다릅니다.\n"+
				"      설정 apiURL : %s\n"+
				"      현재 oc 서버 : %s\n"+
				"      대상 클러스터에 다시 로그인한 뒤 실행하세요", expected, server)
	}

	if _, err := execx.Output(ctx, ocPath, "get", "--raw=/readyz"); err != nil {
		return Result{}, fmt.Errorf("OpenShift API에 접근할 수 없거나 클러스터가 준비되지 않았습니다. " +
			"네트워크, API 주소와 클러스터 상태를 확인하세요")
	}

	logx.Info("OpenShift 클러스터 사전 점검 완료: %s (%s)", user, server)
	return Result{User: user, Server: server}, nil
}

// ClusterVersion은 현재 로그인한 클러스터의 OpenShift 버전을 조회합니다.
// 설정의 cluster.version이 비어 있을 때 사용합니다.
func ClusterVersion(ctx context.Context, ocPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	out, err := execx.Output(ctx, ocPath, "get", "clusterversion",
		"-o", "jsonpath={.items[0].status.desired.version}")
	if err != nil {
		return "", fmt.Errorf("현재 클러스터에서 OpenShift 버전을 조회하지 못했습니다. "+
			"설정에 cluster.version을 지정할 수 있습니다: %w", err)
	}
	version := strings.TrimSpace(out)
	if version == "" {
		return "", fmt.Errorf("조회한 OpenShift 버전이 비어 있습니다. 설정의 cluster.version을 확인하세요")
	}
	return version, nil
}
