// Package ignition은 worker pointer 템플릿에 다음 값을 채워 노드별
// Ignition을 생성합니다.
//
//	__MCS_SOURCE__     -> https://api-int.<domain>:22623/config/worker
//	__MCS_CA_SOURCE__  -> 실행 시점에 MCS가 제시한 인증서 체인(base64 data URL)
//	__NODE_FQDN__      -> 노드 hostname
//
// pathset에 Butane 파일이 있으면 렌더링한 결과의 배열 항목을 템플릿에
// 병합합니다. JSON 처리는 표준 라이브러리로 수행하고, 클러스터에 맞는
// 스펙 버전을 사용하기 위해 Butane 바이너리만 외부 명령으로 실행합니다.
package ignition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"upi-forge/internal/execx"
)

// 템플릿 placeholder 값입니다.
const (
	placeholderMCSSource = "__MCS_SOURCE__"
	placeholderMCSCA     = "__MCS_CA_SOURCE__"
	placeholderNodeFQDN  = "__NODE_FQDN__"

	hostnamePath       = "/etc/hostname"
	hostnameSourceHead = "data:text/plain;charset=utf-8,"
)

// mergeLists는 Butane 결과를 이어 붙일 배열 경로입니다.
var mergeLists = [][]string{
	{"storage", "disks"},
	{"storage", "raid"},
	{"storage", "filesystems"},
	{"storage", "files"},
	{"storage", "directories"},
	{"storage", "links"},
	{"storage", "luks"},
	{"systemd", "units"},
}

// checkButaneSupported는 Butane 결과에 병합 대상(mergeLists) 밖의 내용이
// 있으면 거부합니다. 병합은 등록된 배열만 이어 붙이므로, 그 밖의 필드
// (passwd.users, kernelArguments 등)를 조용히 버리면 운영자는 적용된 것으로
// 믿게 됩니다. 값이 실제로 들어 있는 미지원 필드는 오류로 알립니다.
func checkButaneSupported(root map[string]any) error {
	allowed := map[string]map[string]bool{
		"storage": {"disks": true, "raid": true, "filesystems": true,
			"files": true, "directories": true, "links": true, "luks": true},
		"systemd": {"units": true},
		// ignition 섹션은 버전 메타데이터만 병합과 무관하게 허용합니다.
		"ignition": {"version": true},
	}

	var unsupported []string
	for key, value := range root {
		subAllowed, known := allowed[key]
		if !known {
			if substantiveValue(value) {
				unsupported = append(unsupported, key)
			}
			continue
		}
		section, ok := value.(map[string]any)
		if !ok {
			if substantiveValue(value) {
				unsupported = append(unsupported, key)
			}
			continue
		}
		for sub, v := range section {
			if subAllowed[sub] {
				continue
			}
			if substantiveValue(v) {
				unsupported = append(unsupported, key+"."+sub)
			}
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	sort.Strings(unsupported)
	return fmt.Errorf("Butane 결과에 병합을 지원하지 않는 항목이 있습니다: %s\n"+
		"지원 항목: storage.{disks,raid,filesystems,files,directories,links,luks}, systemd.units\n"+
		"지원 항목 밖의 설정이 조용히 누락되는 것을 막기 위해 병합하지 않고 중단합니다",
		strings.Join(unsupported, ", "))
}

// substantiveValue는 값이 실제 설정을 담고 있는지 판단합니다.
// 빈 객체·빈 배열·빈 문자열·null은 Butane이 만드는 껍데기일 수 있으므로
// 무시하고, 그 밖의 값(숫자, 불리언 포함)은 명시된 설정으로 봅니다.
func substantiveValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case map[string]any:
		for _, sub := range t {
			if substantiveValue(sub) {
				return true
			}
		}
		return false
	case []any:
		return len(t) > 0
	case string:
		return t != ""
	default:
		return true
	}
}

// countedLists는 병합 후 개수가 줄지 않았는지 확인할 배열 경로입니다.
var countedLists = [][]string{
	{"storage", "files"},
	{"storage", "directories"},
	{"systemd", "units"},
}

// Document는 Ignition JSON 문서입니다.
// 템플릿의 알 수 없는 필드도 그대로 보존하기 위해 범용 맵으로 다룹니다.
type Document struct {
	root map[string]any
}

// LoadTemplate은 템플릿 파일을 읽고 필수 placeholder가 있는지 확인합니다.
func LoadTemplate(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Ignition 템플릿을 읽을 수 없습니다: %s: %w", path, err)
	}
	doc, err := parse(raw)
	if err != nil {
		return nil, fmt.Errorf("Ignition 템플릿 JSON 형식이 올바르지 않습니다: %s: %w", path, err)
	}
	if err := doc.validateTemplate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

func parse(raw []byte) (*Document, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // mode: 420 같은 정수가 420 그대로 유지되도록 합니다.
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	// dec.More()는 최상위 문서의 완전 소비를 보장하지 않습니다 — 뒤에
	// '}'나 ']'가 붙으면 false를 반환해 통과합니다. 두 번째 Decode가
	// 정확히 io.EOF일 때만 문서 뒤에 아무것도 없는 것입니다.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("JSON 문서 뒤에 추가 내용이 있습니다")
	}
	return &Document{root: root}, nil
}

func (d *Document) validateTemplate() error {
	source, err := d.mcsSource()
	if err != nil {
		return err
	}
	if source != placeholderMCSSource {
		return fmt.Errorf("템플릿의 필수 placeholder가 없거나 형식이 다릅니다: ignition.config.merge[0].source")
	}
	ca, err := d.caSource()
	if err != nil {
		return err
	}
	if ca != placeholderMCSCA {
		return fmt.Errorf("템플릿의 필수 placeholder가 없거나 형식이 다릅니다: ignition.security.tls.certificateAuthorities[0].source")
	}
	file, err := d.hostnameFile()
	if err != nil {
		return err
	}
	want := hostnameSourceHead + placeholderNodeFQDN
	if got, _ := nestedString(file, "contents", "source"); got != want {
		return fmt.Errorf("템플릿의 필수 placeholder가 없거나 형식이 다릅니다: /etc/hostname contents.source")
	}
	return nil
}

// RenderOptions는 템플릿을 채울 값입니다.
type RenderOptions struct {
	MCSSource string // https://api-int.<domain>:22623/config/worker
	MCSCA     string // data:text/plain;charset=utf-8;base64,...
	Hostname  string // 노드 FQDN
}

// Render는 템플릿 placeholder를 실제 값으로 채운 새 문서를 만듭니다.
func (d *Document) Render(o RenderOptions) (*Document, error) {
	if o.MCSSource == "" || o.MCSCA == "" || o.Hostname == "" {
		return nil, fmt.Errorf("Ignition 렌더에 필요한 값이 비어 있습니다")
	}
	cloned, err := d.clone()
	if err != nil {
		return nil, err
	}

	merge, err := cloned.mergeEntry()
	if err != nil {
		return nil, err
	}
	merge["source"] = o.MCSSource

	ca, err := cloned.caEntry()
	if err != nil {
		return nil, err
	}
	ca["source"] = o.MCSCA

	file, err := cloned.hostnameFile()
	if err != nil {
		return nil, err
	}
	contents, ok := file["contents"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("/etc/hostname 항목에 contents가 없습니다")
	}
	contents["source"] = hostnameSourceHead + o.Hostname

	if err := cloned.validateRendered(o); err != nil {
		return nil, err
	}
	return cloned, nil
}

func (d *Document) validateRendered(o RenderOptions) error {
	source, err := d.mcsSource()
	if err != nil {
		return err
	}
	if source != o.MCSSource {
		return fmt.Errorf("템플릿 렌더 검증에 실패했습니다: merge source가 다릅니다")
	}
	authorities, err := d.caList()
	if err != nil {
		return err
	}
	if len(authorities) != 1 {
		return fmt.Errorf("템플릿 렌더 검증에 실패했습니다: certificateAuthorities 항목은 하나여야 합니다")
	}
	file, err := d.hostnameFile()
	if err != nil {
		return err
	}
	got, _ := nestedString(file, "contents", "source")
	if !strings.HasSuffix(got, o.Hostname) {
		return fmt.Errorf("템플릿 렌더 검증에 실패했습니다: /etc/hostname 값이 다릅니다")
	}
	if left := d.remainingPlaceholders(); len(left) > 0 {
		return fmt.Errorf("템플릿 렌더 검증에 실패했습니다: 치환되지 않은 placeholder가 남아 있습니다: %s",
			strings.Join(left, ", "))
	}
	return nil
}

// MergeButane은 butane으로 렌더한 pathset Ignition을 이어 붙입니다.
// Ignition 스펙 버전이 다르면 병합하지 않고 중단합니다.
func (d *Document) MergeButane(ctx context.Context, butanePath, butaneFile string) error {
	if _, err := execx.Require(butanePath); err != nil {
		return err
	}
	var out bytes.Buffer
	if err := execx.RunTo(ctx, &out, butanePath, "--strict", "--pretty", butaneFile); err != nil {
		return fmt.Errorf("Butane 렌더에 실패했습니다: %s: %w", butaneFile, err)
	}
	overlay, err := parse(out.Bytes())
	if err != nil {
		return fmt.Errorf("Butane 결과 JSON 형식이 올바르지 않습니다: %s: %w", butaneFile, err)
	}

	baseVersion, _ := nestedString(d.root, "ignition", "version")
	overlayVersion, _ := nestedString(overlay.root, "ignition", "version")
	if baseVersion == "" || overlayVersion == "" {
		return fmt.Errorf("Ignition 버전을 확인하지 못했습니다 (base=%q, pathset=%q)", baseVersion, overlayVersion)
	}
	if baseVersion != overlayVersion {
		return fmt.Errorf("Ignition 버전이 일치하지 않습니다 (base=%s, pathset=%s)", baseVersion, overlayVersion)
	}
	if err := checkButaneSupported(overlay.root); err != nil {
		return fmt.Errorf("%s: %w", butaneFile, err)
	}

	before := map[string]int{}
	for _, path := range countedLists {
		before[strings.Join(path, ".")] = len(listAt(d.root, path))
	}

	for _, path := range mergeLists {
		merged := append(append([]any{}, listAt(d.root, path)...), listAt(overlay.root, path)...)
		if len(merged) == 0 {
			continue
		}
		setListAt(d.root, path, merged)
	}

	for _, path := range countedLists {
		key := strings.Join(path, ".")
		if len(listAt(d.root, path)) < before[key] {
			return fmt.Errorf("병합 후 %s 개수가 감소했습니다", key)
		}
	}
	return nil
}

// Validate는 최종 Ignition이 조건을 만족하는지 확인합니다.
func (d *Document) Validate(expectedMCSSource string) error {
	if left := d.remainingPlaceholders(); len(left) > 0 {
		return fmt.Errorf("최종 Ignition 검증에 실패했습니다: placeholder가 남아 있습니다: %s",
			strings.Join(left, ", "))
	}
	// Ignition v3 스펙은 storage.files의 path 중복을 허용하지 않습니다.
	// Butane 병합이 템플릿과 같은 경로(/etc/hostname 등)를 추가하면
	// 노드 부팅 시점에야 실패하므로, 생성 시점에 잡습니다.
	if dups := d.duplicateFilePaths(); len(dups) > 0 {
		return fmt.Errorf("최종 Ignition 검증에 실패했습니다: storage.files에 중복 path가 있습니다: %s (pathset Butane이 템플릿과 같은 파일을 정의했는지 확인하세요)",
			strings.Join(dups, ", "))
	}
	source, err := d.mcsSource()
	if err != nil {
		return fmt.Errorf("최종 Ignition 검증에 실패했습니다: %w", err)
	}
	if source != expectedMCSSource {
		return fmt.Errorf("최종 Ignition 검증에 실패했습니다: merge source가 %s가 아닙니다", expectedMCSSource)
	}
	return nil
}

// Bytes는 저장할 JSON 바이트를 만듭니다.
func (d *Document) Bytes() ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d.root); err != nil {
		return nil, fmt.Errorf("Ignition JSON을 직렬화하지 못했습니다: %w", err)
	}
	return buf.Bytes(), nil
}

// Artifact는 기존에 생성된 노드 Ignition에서 읽은 출처 정보입니다.
// iso가 산출물 재사용 전에 현재 설정·클러스터와 대조하는 데 씁니다.
type Artifact struct {
	Hostname  string // /etc/hostname에 들어갈 값
	MCSSource string // ignition.config.merge[0].source
	CASource  string // certificateAuthorities[0].source (data URL)
}

// InspectArtifact는 기존 <hostname>.ign 파일에서 출처 정보를 읽습니다.
// 같은 이름의 파일도 다른 노드나 클러스터에서 생성됐을 수 있으므로 JSON
// 형식뿐 아니라 hostname, MCS 주소와 CA도 확인합니다.
func InspectArtifact(path string) (*Artifact, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Ignition 파일을 읽지 못했습니다: %s: %w", path, err)
	}
	doc, err := parse(raw)
	if err != nil {
		return nil, fmt.Errorf("Ignition JSON 형식이 올바르지 않습니다: %s: %w", path, err)
	}
	// 이 도구가 만드는 Ignition은 merge와 certificateAuthorities가 정확히
	// 하나씩입니다(validateRendered가 보장). 첫 항목만 검사하면 두 번째
	// 항목으로 다른 MCS나 다른 CA를 끼워 넣은 산출물이 통과하므로
	// 항목 수도 정확히 확인합니다.
	if list := listAt(doc.root, []string{"ignition", "config", "merge"}); len(list) != 1 {
		return nil, fmt.Errorf("%s: ignition.config.merge 항목은 정확히 하나여야 합니다 (%d개) — 이 도구가 만든 산출물이 아닙니다", path, len(list))
	}
	if list := listAt(doc.root, []string{"ignition", "security", "tls", "certificateAuthorities"}); len(list) != 1 {
		return nil, fmt.Errorf("%s: certificateAuthorities 항목은 정확히 하나여야 합니다 (%d개) — 이 도구가 만든 산출물이 아닙니다", path, len(list))
	}
	// /etc/hostname이 중복이면 첫 항목만 검사한 결과가 실제 적용값과
	// 다를 수 있습니다. Ignition v3 스펙도 path 중복을 허용하지
	// 않으므로 이 도구의 산출물이 아닙니다.
	if dups := doc.duplicateFilePaths(); len(dups) > 0 {
		return nil, fmt.Errorf("%s: storage.files에 중복 path가 있습니다: %s — 이 도구가 만든 산출물이 아닙니다", path, strings.Join(dups, ", "))
	}
	source, err := doc.mcsSource()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if source == "" {
		return nil, fmt.Errorf("%s: merge source가 비어 있습니다", path)
	}
	ca, err := doc.caSource()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ca == "" {
		return nil, fmt.Errorf("%s: certificateAuthorities source가 비어 있습니다", path)
	}
	file, err := doc.hostnameFile()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	hostSource, _ := nestedString(file, "contents", "source")
	if !strings.HasPrefix(hostSource, hostnameSourceHead) {
		return nil, fmt.Errorf("%s: /etc/hostname contents.source 형식이 올바르지 않습니다: %q", path, hostSource)
	}
	return &Artifact{
		Hostname:  strings.TrimPrefix(hostSource, hostnameSourceHead),
		MCSSource: source,
		CASource:  ca,
	}, nil
}

// --- 내부 접근 도우미 -------------------------------------------------------

func (d *Document) clone() (*Document, error) {
	raw, err := json.Marshal(d.root)
	if err != nil {
		return nil, fmt.Errorf("Ignition 문서를 복제하지 못했습니다: %w", err)
	}
	return parse(raw)
}

func (d *Document) mergeEntry() (map[string]any, error) {
	list := listAt(d.root, []string{"ignition", "config", "merge"})
	if len(list) == 0 {
		return nil, fmt.Errorf("ignition.config.merge 항목이 없습니다")
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ignition.config.merge[0] 형식이 올바르지 않습니다")
	}
	return entry, nil
}

func (d *Document) mcsSource() (string, error) {
	entry, err := d.mergeEntry()
	if err != nil {
		return "", err
	}
	source, _ := entry["source"].(string)
	return source, nil
}

func (d *Document) caList() ([]any, error) {
	list := listAt(d.root, []string{"ignition", "security", "tls", "certificateAuthorities"})
	if len(list) == 0 {
		return nil, fmt.Errorf("ignition.security.tls.certificateAuthorities 항목이 없습니다")
	}
	return list, nil
}

func (d *Document) caEntry() (map[string]any, error) {
	list, err := d.caList()
	if err != nil {
		return nil, err
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("certificateAuthorities[0] 형식이 올바르지 않습니다")
	}
	return entry, nil
}

func (d *Document) caSource() (string, error) {
	entry, err := d.caEntry()
	if err != nil {
		return "", err
	}
	source, _ := entry["source"].(string)
	return source, nil
}

func (d *Document) hostnameFile() (map[string]any, error) {
	for _, item := range listAt(d.root, []string{"storage", "files"}) {
		file, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if path, _ := file["path"].(string); path == hostnamePath {
			return file, nil
		}
	}
	return nil, fmt.Errorf("storage.files에 %s 항목이 없습니다", hostnamePath)
}

// duplicateFilePaths는 storage.files에서 중복된 path를 반환합니다.
// Ignition v3 스펙은 같은 path의 파일 항목 중복을 허용하지 않습니다.
func (d *Document) duplicateFilePaths() []string {
	seen := map[string]bool{}
	var dups []string
	for _, item := range listAt(d.root, []string{"storage", "files"}) {
		file, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := file["path"].(string)
		if path == "" {
			continue
		}
		if seen[path] {
			dups = append(dups, path)
		}
		seen[path] = true
	}
	return dups
}

// remainingPlaceholders는 문서에 "__"가 포함된 문자열이 남아 있는지 찾습니다.
func (d *Document) remainingPlaceholders() []string {
	var found []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if strings.Contains(t, "__") {
				found = append(found, t)
			}
		case map[string]any:
			for _, item := range t {
				walk(item)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(d.root)
	return found
}

// listAt은 중첩 경로의 배열을 반환합니다. 없으면 nil입니다.
func listAt(root map[string]any, path []string) []any {
	cur := any(root)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[key]
		if !ok {
			return nil
		}
	}
	list, _ := cur.([]any)
	return list
}

// setListAt은 중첩 경로에 배열을 설정합니다. 중간 맵이 없으면 만듭니다.
func setListAt(root map[string]any, path []string, value []any) {
	cur := root
	for _, key := range path[:len(path)-1] {
		next, ok := cur[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
}

// nestedString은 중첩 경로의 문자열 값을 반환합니다.
func nestedString(root map[string]any, path ...string) (string, bool) {
	cur := any(root)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[key]
		if !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}
