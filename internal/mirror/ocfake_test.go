package mirror

// TestMain은 테스트 바이너리를 다시 실행해 Windows에서도 사용할 수 있는
// oc 대역을 제공합니다. 별도 프로세스로 조회 제한 시간과 취소 동작을 검증합니다.
import (
	"fmt"
	"os"
	"testing"
	"time"
)

const (
	envFakeOCMirror = "UPI_FORGE_TEST_FAKE_OC_MIRROR"
	envHang         = "UPI_FORGE_TEST_OC_HANG"
	envForbid       = "UPI_FORGE_TEST_OC_FORBID"
)

func TestMain(m *testing.M) {
	if os.Getenv(envFakeOCMirror) != "1" {
		os.Exit(m.Run())
	}
	if os.Getenv(envForbid) == "1" {
		// RBAC에 의해 조회가 거부되는 경우를 재현합니다.
		fmt.Fprintln(os.Stderr, "Error from server (Forbidden): ... is forbidden")
		os.Exit(1)
	}
	if os.Getenv(envHang) == "1" {
		// oc 조회가 응답하지 않는 경우를 재현합니다. 제한 시간이 지나면
		// CommandContext가 이 프로세스를 종료해야 합니다.
		time.Sleep(30 * time.Second)
	}
	fmt.Println(`{"items":[]}`)
	os.Exit(0)
}
