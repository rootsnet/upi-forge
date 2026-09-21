package ignition_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"upi-forge/internal/ignition"
)

// 운영에서 사용하는 templates/worker-pointer.ign.template과 같은 내용입니다.
const templateJSON = `{
  "ignition": {
    "version": "3.5.0",
    "config": { "merge": [ { "source": "__MCS_SOURCE__" } ] },
    "security": {
      "tls": { "certificateAuthorities": [ { "source": "__MCS_CA_SOURCE__" } ] }
    }
  },
  "storage": {
    "files": [
      {
        "overwrite": true,
        "path": "/etc/hostname",
        "user": { "name": "root" },
        "contents": { "source": "data:text/plain;charset=utf-8,__NODE_FQDN__" },
        "mode": 420
      }
    ]
  }
}`

const mcsSource = "https://api-int.mycluster.example.com:22623/config/worker"
const caSource = "data:text/plain;charset=utf-8;base64,QUJD"

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("파일 생성 실패: %v", err)
	}
	return path
}

func TestLoadTemplateRejectsMissingPlaceholders(t *testing.T) {
	if _, err := ignition.LoadTemplate(writeFile(t, "t.json", `{"ignition":{"version":"3.5.0"}}`)); err == nil {
		t.Error("placeholder가 없으면 오류여야 합니다")
	}
	if _, err := ignition.LoadTemplate(writeFile(t, "bad.json", `{not json`)); err == nil {
		t.Error("잘못된 JSON은 오류여야 합니다")
	}
	if _, err := ignition.LoadTemplate(writeFile(t, "ok.json", templateJSON)); err != nil {
		t.Errorf("정상 템플릿을 거부했습니다: %v", err)
	}
}

func TestRenderFillsPlaceholders(t *testing.T) {
	template, err := ignition.LoadTemplate(writeFile(t, "t.json", templateJSON))
	if err != nil {
		t.Fatalf("LoadTemplate 실패: %v", err)
	}

	doc, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource,
		MCSCA:     caSource,
		Hostname:  "worker2.other.example.com",
	})
	if err != nil {
		t.Fatalf("Render 실패: %v", err)
	}
	if err := doc.Validate(mcsSource); err != nil {
		t.Fatalf("Validate 실패: %v", err)
	}

	data, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes 실패: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "__") {
		t.Errorf("치환되지 않은 placeholder가 남았습니다:\n%s", text)
	}
	if !strings.Contains(text, mcsSource) || !strings.Contains(text, caSource) {
		t.Errorf("MCS 값이 반영되지 않았습니다:\n%s", text)
	}
	if !strings.Contains(text, "data:text/plain;charset=utf-8,worker2.other.example.com") {
		t.Errorf("hostname이 반영되지 않았습니다:\n%s", text)
	}
	// mode: 420은 정수 그대로 유지되어야 합니다(부동소수점으로 바뀌면 안 됩니다).
	if !strings.Contains(text, `"mode": 420`) {
		t.Errorf("정수 필드가 변형되었습니다:\n%s", text)
	}

	// 원본 템플릿은 그대로 남아 다음 노드에 재사용할 수 있어야 합니다.
	if err := doc.Validate(mcsSource); err != nil {
		t.Fatalf("두 번째 Validate 실패: %v", err)
	}
	second, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource, MCSCA: caSource, Hostname: "worker3.other.example.com",
	})
	if err != nil {
		t.Fatalf("두 번째 Render 실패: %v", err)
	}
	secondData, _ := second.Bytes()
	if strings.Contains(string(secondData), "worker2.other.example.com") {
		t.Error("템플릿이 이전 노드 값으로 오염되었습니다")
	}
}

// envButaneOutput이 설정되어 있으면 이 테스트 바이너리가 butane 대역으로 동작합니다.
const envButaneOutput = "UPI_FORGE_TEST_BUTANE_OUTPUT"

// TestMain은 butane 대역 역할을 겸합니다.
//
// 셸 스크립트로 대역을 만들면 Windows에서 실행되지 않아 테스트가 플랫폼에
// 묶입니다. 대신 테스트 바이너리 자신을 butane 자리에 놓고, 환경 변수가
// 있으면 지정한 JSON만 출력하고 종료하게 합니다. 플래그 파싱 전에 실행되므로
// butane이 받는 --strict 같은 인자에도 영향을 받지 않습니다.
func TestMain(m *testing.M) {
	if output := os.Getenv(envButaneOutput); output != "" {
		fmt.Println(output)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeButane은 고정된 Ignition JSON을 출력하는 butane 대역 경로를 반환합니다.
// 어떤 OS에서도 동작하도록 테스트 바이너리 자신을 사용합니다.
func fakeButane(t *testing.T, output string) string {
	t.Helper()
	t.Setenv(envButaneOutput, output)
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("테스트 바이너리 경로를 확인하지 못했습니다: %v", err)
	}
	return path
}

const butaneOutput = `{
  "ignition": { "version": "3.5.0" },
  "storage": {
    "disks": [ { "device": "/dev/disk/by-path/pci-0000:ae:00.0-nvme-1" } ],
    "raid": [ { "name": "md-var-lib-containers", "level": "raid1" } ],
    "filesystems": [ { "device": "/dev/md/md-var-lib-containers", "path": "/var/lib/containers" } ]
  },
  "systemd": { "units": [ { "name": "var-lib-containers.mount", "enabled": true } ] }
}`

func TestMergeButaneConcatenatesLists(t *testing.T) {
	template, err := ignition.LoadTemplate(writeFile(t, "t.json", templateJSON))
	if err != nil {
		t.Fatalf("LoadTemplate 실패: %v", err)
	}
	doc, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource, MCSCA: caSource, Hostname: "worker2.other.example.com",
	})
	if err != nil {
		t.Fatalf("Render 실패: %v", err)
	}

	butane := fakeButane(t, butaneOutput)
	if err := doc.MergeButane(context.Background(), butane, "ignored.bu"); err != nil {
		t.Fatalf("MergeButane 실패: %v", err)
	}
	if err := doc.Validate(mcsSource); err != nil {
		t.Fatalf("병합 후 Validate 실패: %v", err)
	}

	data, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes 실패: %v", err)
	}
	var parsed struct {
		Storage struct {
			Disks       []any `json:"disks"`
			RAID        []any `json:"raid"`
			Filesystems []any `json:"filesystems"`
			Files       []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"storage"`
		Systemd struct {
			Units []any `json:"units"`
		} `json:"systemd"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("병합 결과 파싱 실패: %v", err)
	}
	if len(parsed.Storage.Disks) != 1 || len(parsed.Storage.RAID) != 1 || len(parsed.Storage.Filesystems) != 1 {
		t.Errorf("Butane 항목이 병합되지 않았습니다: %+v", parsed.Storage)
	}
	if len(parsed.Systemd.Units) != 1 {
		t.Errorf("systemd 유닛이 병합되지 않았습니다: %+v", parsed.Systemd)
	}
	// base의 /etc/hostname은 반드시 살아 있어야 합니다.
	found := false
	for _, f := range parsed.Storage.Files {
		if f.Path == "/etc/hostname" {
			found = true
		}
	}
	if !found {
		t.Error("병합 후 /etc/hostname 파일이 사라졌습니다")
	}
}

func TestMergeButaneRejectsVersionMismatch(t *testing.T) {
	template, err := ignition.LoadTemplate(writeFile(t, "t.json", templateJSON))
	if err != nil {
		t.Fatalf("LoadTemplate 실패: %v", err)
	}
	doc, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource, MCSCA: caSource, Hostname: "worker2.other.example.com",
	})
	if err != nil {
		t.Fatalf("Render 실패: %v", err)
	}

	butane := fakeButane(t, strings.Replace(butaneOutput, "3.5.0", "3.4.0", 1))
	err = doc.MergeButane(context.Background(), butane, "ignored.bu")
	if err == nil {
		t.Fatal("Ignition 버전이 다르면 오류여야 합니다")
	}
	if !strings.Contains(err.Error(), "버전이 일치하지 않습니다") {
		t.Errorf("오류 메시지가 예상과 다릅니다: %v", err)
	}
}

func TestValidateDetectsWrongMCSSource(t *testing.T) {
	template, err := ignition.LoadTemplate(writeFile(t, "t.json", templateJSON))
	if err != nil {
		t.Fatalf("LoadTemplate 실패: %v", err)
	}
	doc, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource, MCSCA: caSource, Hostname: "worker2.other.example.com",
	})
	if err != nil {
		t.Fatalf("Render 실패: %v", err)
	}
	if err := doc.Validate("https://api-int.other.example.com:22623/config/worker"); err == nil {
		t.Error("다른 클러스터의 MCS면 오류여야 합니다")
	}
}

// Ignition v3 스펙은 storage.files의 path 중복을
// 허용하지 않습니다. Butane이 템플릿과 같은 경로(/etc/hostname 등)를
// 정의하면 노드 부팅 시점이 아니라 생성 시점(Validate)에 잡아야 합니다.
func TestValidateRejectsDuplicateFilePaths(t *testing.T) {
	template, err := ignition.LoadTemplate(writeFile(t, "t.json", templateJSON))
	if err != nil {
		t.Fatalf("LoadTemplate 실패: %v", err)
	}
	doc, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource, MCSCA: caSource, Hostname: "worker2.other.example.com",
	})
	if err != nil {
		t.Fatalf("Render 실패: %v", err)
	}

	// butane 대역이 /etc/hostname을 한 번 더 정의합니다.
	const dupOutput = `{
  "ignition": { "version": "3.5.0" },
  "storage": { "files": [ { "path": "/etc/hostname",
    "contents": { "source": "data:text/plain;charset=utf-8,evil" } } ] }
}`
	butane := fakeButane(t, dupOutput)
	if err := doc.MergeButane(context.Background(), butane, "ignored.bu"); err != nil {
		t.Fatalf("MergeButane 실패: %v", err)
	}
	err = doc.Validate(mcsSource)
	if err == nil || !strings.Contains(err.Error(), "중복 path") {
		t.Fatalf("중복 /etc/hostname은 생성 시점에 거부되어야 합니다: %v", err)
	}
}

// 회귀 테스트: dec.More()는 최상위 문서의 완전
// 소비를 보장하지 않아, 유효한 JSON 뒤에 '}'를 붙이면 통과했습니다.
// 문서 뒤에 무엇이 붙어 있든 거부해야 합니다.
func TestLoadTemplateRejectsTrailingData(t *testing.T) {
	cases := map[string]string{
		"닫는 괄호":   "}", // dec.More()가 false를 반환하던 우회 사례
		"닫는 대괄호":  "]",
		"두 번째 문서": `{"a":1}`,
		"임의 문자열":  "garbage",
	}
	for name, suffix := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, "t.json", templateJSON+"\n"+suffix)
			_, err := ignition.LoadTemplate(path)
			if err == nil || !strings.Contains(err.Error(), "형식이 올바르지 않습니다") {
				t.Fatalf("문서 뒤의 %q는 거부되어야 합니다: %v", suffix, err)
			}
		})
	}
}

// renderedDoc은 Butane 병합 테스트용으로 렌더까지 끝난 문서를 만듭니다.
func renderedDoc(t *testing.T) *ignition.Document {
	t.Helper()
	template, err := ignition.LoadTemplate(writeFile(t, "t.json", templateJSON))
	if err != nil {
		t.Fatalf("LoadTemplate 실패: %v", err)
	}
	doc, err := template.Render(ignition.RenderOptions{
		MCSSource: mcsSource, MCSCA: caSource, Hostname: "worker2.other.example.com",
	})
	if err != nil {
		t.Fatalf("Render 실패: %v", err)
	}
	return doc
}

// 병합은 등록된 배열(storage.*, systemd.units)만 이어 붙이므로, 그 밖의
// 정상 Ignition 필드가 Butane 결과에 있으면 조용히 누락되는 대신 명확히
// 거부해야 합니다.
func TestMergeButaneRejectsUnsupportedContent(t *testing.T) {
	cases := map[string]struct {
		output   string
		wantPath string
	}{
		"passwd.users": {
			output:   `{"ignition":{"version":"3.5.0"},"passwd":{"users":[{"name":"core"}]}}`,
			wantPath: "passwd",
		},
		"kernelArguments": {
			output:   `{"ignition":{"version":"3.5.0"},"kernelArguments":{"shouldExist":["mitigations=off"]}}`,
			wantPath: "kernelArguments",
		},
		"ignition.proxy": {
			output:   `{"ignition":{"version":"3.5.0","proxy":{"httpProxy":"http://proxy.example.com:3128"}}}`,
			wantPath: "ignition.proxy",
		},
		"storage.trees": {
			output:   `{"ignition":{"version":"3.5.0"},"storage":{"trees":[{"local":"x"}]}}`,
			wantPath: "storage.trees",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc := renderedDoc(t)
			err := doc.MergeButane(context.Background(), fakeButane(t, tc.output), "ignored.bu")
			if err == nil {
				t.Fatal("미지원 필드가 있으면 병합을 거부해야 합니다")
			}
			if !strings.Contains(err.Error(), "지원하지 않는 항목") ||
				!strings.Contains(err.Error(), tc.wantPath) {
				t.Errorf("오류에 문제의 경로가 있어야 합니다: %v", err)
			}
			// 지원 범위 안내가 있어야 운영자가 대안을 찾을 수 있습니다.
			if !strings.Contains(err.Error(), "systemd.units") {
				t.Errorf("오류에 지원 항목 목록이 있어야 합니다: %v", err)
			}
		})
	}
}

// Butane이 만드는 빈 껍데기 섹션(빈 객체·빈 배열)은 미지원 설정으로
// 오인하지 않아야 합니다.
func TestMergeButaneToleratesEmptySections(t *testing.T) {
	output := `{
  "ignition": { "version": "3.5.0", "config": {}, "timeouts": {} },
  "passwd": {},
  "kernelArguments": { "shouldExist": [] },
  "storage": { "files": [ { "path": "/etc/example", "mode": 420 } ] }
}`
	doc := renderedDoc(t)
	if err := doc.MergeButane(context.Background(), fakeButane(t, output), "ignored.bu"); err != nil {
		t.Fatalf("빈 섹션은 병합을 막으면 안 됩니다: %v", err)
	}
}
