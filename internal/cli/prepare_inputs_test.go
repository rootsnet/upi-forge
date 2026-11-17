package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"upi-forge/internal/mirror"
)

// 대상 클러스터와 버전이 일치하면 조회한 버전·desired 이미지·미러 설정을
// 사용하는지 확인합니다.
func TestResolvePrepareInputsVerified(t *testing.T) {
	loaded := &mirror.Sets{}
	ver, img, sets, err := resolvePrepareInputs(context.Background(), "4.22.5", "quay.io", "quay.io",
		clusterProbe{
			check:    func(context.Context) error { return nil },
			version:  func(context.Context) (string, error) { return "4.22.5", nil },
			loadSets: func(context.Context) (*mirror.Sets, error) { return loaded, nil },
			desiredImage: func(context.Context) (string, error) {
				return "quay.io/openshift-release-dev/ocp-release@sha256:abc", nil
			},
		})
	if err != nil {
		t.Fatalf("예상치 못한 오류: %v", err)
	}
	if ver != "4.22.5" || img != "quay.io/openshift-release-dev/ocp-release@sha256:abc" || sets != loaded {
		t.Errorf("검증 모드 결과: ver=%q img=%q sets=%v", ver, img, sets)
	}
}

// 대상 클러스터가 맞더라도 지정한 버전이 실제 버전과 다르면
// 오류로 중단하는지 확인합니다.
func TestResolvePrepareInputsVersionMismatch(t *testing.T) {
	_, _, _, err := resolvePrepareInputs(context.Background(), "4.20.0", "quay.io", "quay.io",
		clusterProbe{
			check:        func(context.Context) error { return nil },
			version:      func(context.Context) (string, error) { return "4.22.5", nil },
			loadSets:     func(context.Context) (*mirror.Sets, error) { t.Fatal("미러를 읽으면 안 됨"); return nil, nil },
			desiredImage: func(context.Context) (string, error) { return "", nil },
		})
	if err == nil || !strings.Contains(err.Error(), "다릅니다") {
		t.Fatalf("버전 불일치는 오류여야 합니다: %v", err)
	}
}

// 대상 클러스터를 확인하지 못했지만 버전이 지정되어 있으면, 클러스터의 미러
// 설정을 사용하지 않고(sets=nil) 지정한 버전으로 이미지 참조를 구성하는지 확인합니다.
func TestResolvePrepareInputsOfflineWithVersion(t *testing.T) {
	ver, img, sets, err := resolvePrepareInputs(context.Background(), "4.22.5",
		"mirror.local:8443", "mirror.local:8443",
		clusterProbe{
			check:        func(context.Context) error { return errors.New("다른 클러스터") },
			version:      func(context.Context) (string, error) { t.Fatal("버전 조회하면 안 됨"); return "", nil },
			loadSets:     func(context.Context) (*mirror.Sets, error) { t.Fatal("미러를 읽으면 안 됨"); return nil, nil },
			desiredImage: func(context.Context) (string, error) { t.Fatal("desired 조회하면 안 됨"); return "", nil },
		})
	if err != nil {
		t.Fatalf("오프라인 폴백은 오류가 아니어야 합니다: %v", err)
	}
	if sets != nil {
		t.Error("검증되지 않은 클러스터의 미러 설정을 적용하면 안 됩니다")
	}
	if ver != "4.22.5" || img != "mirror.local:8443/openshift/release-images:4.22.5-x86_64" {
		t.Errorf("오프라인 구성 결과: ver=%q img=%q", ver, img)
	}
}

// 대상 클러스터를 확인하지 못하고 지정한 버전도 없으면 오류로 중단하는지 확인합니다.
func TestResolvePrepareInputsNoVersionNoCluster(t *testing.T) {
	sentinel := errors.New("로그인 필요")
	_, _, _, err := resolvePrepareInputs(context.Background(), "", "quay.io", "quay.io",
		clusterProbe{
			check:   func(context.Context) error { return sentinel },
			version: func(context.Context) (string, error) { return "", nil },
		})
	if !errors.Is(err, sentinel) {
		t.Fatalf("버전도 클러스터도 없으면 검증 오류로 중단해야 합니다: %v", err)
	}
}

// 미러 설정 조회가 상위 컨텍스트 취소 오류를 반환하면 그대로 중단하는지 확인합니다.
func TestResolvePrepareInputsLoadCancel(t *testing.T) {
	_, _, _, err := resolvePrepareInputs(context.Background(), "4.22.5", "quay.io", "quay.io",
		clusterProbe{
			check:    func(context.Context) error { return nil },
			version:  func(context.Context) (string, error) { return "4.22.5", nil },
			loadSets: func(context.Context) (*mirror.Sets, error) { return nil, context.Canceled },
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("취소는 그대로 중단해야 합니다: %v", err)
	}
}

// 대상 클러스터를 확인했더라도 desired 이미지를 읽지 못하면
// 버전으로 이미지 참조를 구성하는지 확인합니다.
func TestResolvePrepareInputsDesiredImageFallback(t *testing.T) {
	_, img, _, err := resolvePrepareInputs(context.Background(), "", "quay.io", "quay.io",
		clusterProbe{
			check:        func(context.Context) error { return nil },
			version:      func(context.Context) (string, error) { return "4.22.5", nil },
			loadSets:     func(context.Context) (*mirror.Sets, error) { return &mirror.Sets{}, nil },
			desiredImage: func(context.Context) (string, error) { return "", errors.New("권한 없음") },
		})
	if err != nil {
		t.Fatalf("desired 실패는 폴백이어야 합니다: %v", err)
	}
	if img != "quay.io/openshift-release-dev/ocp-release:4.22.5-x86_64" {
		t.Errorf("버전 구성 폴백 결과: %q", img)
	}
}
