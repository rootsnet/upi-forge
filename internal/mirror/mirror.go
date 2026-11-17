// Package mirror는 폐쇄망(disconnected) 클러스터의 이미지 미러 설정
// (ImageDigestMirrorSet, ImageTagMirrorSet, 구형 ImageContentSourcePolicy)을
// 읽어 인터넷 레지스트리 주소(quay.io, registry.redhat.io 등)를 그 설정이
// 가리키는 미러 레지스트리 주소로 바꿉니다.
//
// 폐쇄망 클러스터에서 release 이미지나 machine-os-images 참조를 조회하면,
// 미러 주소가 아니라 원본 인터넷 주소가 나올 수 있습니다.
// 클러스터는 CRI-O 미러 설정에 따라 이미지를 받지만
// bastion의 oc는 그 설정을 자동으로 따르지 않으므로, 이 패키지에서 미러 규칙으로
// 후보 주소를 만들어 호출부가 순서대로 시도합니다.
//
// 미러 규칙 적용 방식:
//   - source는 레지스트리/저장소 경로의 접두사(경로 요소 단위)이거나,
//     `*.example.com` 형태의 호스트 와일드카드입니다. 정확한 경로 접두사가
//     와일드카드보다 우선하며, 같은 유형에서는 더 구체적인 source를 선택합니다.
//   - 다이제스트(@sha256) 참조는 IDMS/ICSP, 태그 참조는 ITMS가 적용됩니다.
//     적용 대상 규칙 중 source가 같으면 mirror 목록을 병합합니다.
//   - mirrorSourcePolicy가 NeverContactSource이면 원본 주소를 후보에
//     넣지 않습니다. 그 밖에는 모든 미러 뒤에 원본을 마지막 후보로 둡니다.
package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"upi-forge/internal/execx"
	"upi-forge/internal/logx"
)

// queryTimeout은 미러 설정 조회 전체에 적용하는 제한 시간입니다. 용량이
// 큰 이미지 추출과 구분되며, API가 응답하지 않을 때 폴백으로 넘어가도록
// 합니다. 테스트에서는 짧은 제한 시간으로 바꿀 수 있습니다.
var queryTimeout = 20 * time.Second

// mapping은 하나의 미러 규칙입니다. mirrors는 우선순위에 따라 정렬된 목록입니다.
type mapping struct {
	source   string
	mirrors  []string
	noSource bool // mirrorSourcePolicy == NeverContactSource
	wild     bool // source가 `*.suffix` 호스트 와일드카드
}

// Sets는 클러스터에서 읽은 미러 규칙 모음입니다.
// digest는 다이제스트 참조에, tag는 태그 참조에 적용됩니다.
type Sets struct {
	digest   []mapping
	tag      []mapping
	reliable bool // 조회·해석 실패가 없으면 true(리소스 종류가 없는 경우는 정상)
}

// Empty는 적용할 미러 규칙이 없으면 true를 반환합니다.
func (s *Sets) Empty() bool { return s == nil || (len(s.digest) == 0 && len(s.tag) == 0) }

// Reliable은 미러 설정 조회·해석에 실패하지 않았으면 true를 반환합니다.
// 일부 리소스를 읽지 못했거나 Sets가 nil이면 false입니다. 읽지 못한 리소스에
// 규칙이 있을 수 있으므로, 호출부는 이 경우 매칭되는 규칙이 없으면 폴백을 적용합니다.
func (s *Sets) Reliable() bool { return s != nil && s.reliable }

type loadStatus int

const (
	loadOK       loadStatus = iota // 조회·해석 성공(빈 목록 포함)
	loadAbsent                     // 리소스 종류 없음(환경·버전에 따른 차이) — 정상
	loadRealFail                   // 권한·연결·시간 초과 등 실제 실패
	loadCanceled                   // 상위 컨텍스트 취소(Ctrl+C 등)
)

// Load는 클러스터에서 IDMS/ITMS/ICSP를 oc로 읽어옵니다. 조회 전체에
// queryTimeout을 적용합니다. 상위 컨텍스트가 취소되면(Ctrl+C) 오류를
// 반환해 호출부가 중단하도록 합니다. 그 밖의 조회·해석 실패는 경고를 남기고,
// 읽은 규칙만 반환합니다. 리소스 종류가 없는 경우는 정상으로 처리합니다.
//
// 호출 전제: 이 함수는 대상 클러스터(cluster.apiURL)와 현재 oc 로그인
// 대상이 일치함을 확인한 뒤에만 호출해야 합니다. 그렇지 않으면 다른
// 클러스터의 미러 설정을 적용할 수 있습니다.
func Load(ctx context.Context, ocPath string) (*Sets, error) {
	qctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	s := &Sets{}
	realFail := false
	var unsupported []string

	resources := []struct {
		name   string
		field  string
		digest bool
	}{
		{"imagedigestmirrorset", "imageDigestMirrors", true},
		{"imagecontentsourcepolicy", "repositoryDigestMirrors", true},
		{"imagetagmirrorset", "imageTagMirrors", false},
	}
	for _, r := range resources {
		items, unsup, status := getMirrors(ctx, qctx, ocPath, r.name, r.field)
		switch status {
		case loadCanceled:
			return nil, ctx.Err()
		case loadRealFail:
			realFail = true
		}
		unsupported = append(unsupported, unsup...)
		if r.digest {
			s.digest = append(s.digest, items...)
		} else {
			s.tag = append(s.tag, items...)
		}
	}

	for _, u := range unsupported {
		logx.Warn("지원하지 않는 미러 source 형식이라 적용하지 않습니다(원본 주소가 그대로 쓰입니다): %s", u)
	}
	if realFail {
		logx.Warn("일부 이미지 미러 설정을 읽지 못했습니다(권한·연결·시간 초과). " +
			"미러 주소 변환이 불완전할 수 있으니 oc 로그인 상태와 권한을 확인하세요. " +
			"매칭되는 규칙이 없으면 기존 폴백 방식으로 진행합니다.")
	}
	s.reliable = !realFail
	return s, nil
}

// getMirrors는 한 종류의 미러 리소스를 JSON으로 읽어 파싱합니다.
func getMirrors(parent, qctx context.Context, ocPath, resource, field string) ([]mapping, []string, loadStatus) {
	out, err := execx.Output(qctx, ocPath, "get", resource, "-o", "json")
	if err != nil {
		if parent.Err() != nil {
			return nil, nil, loadCanceled // 상위 취소
		}
		if errors.Is(qctx.Err(), context.DeadlineExceeded) {
			logx.Warn("%s 조회가 제한 시간(%s)을 넘겼습니다. 건너뜁니다.", resource, queryTimeout)
			return nil, nil, loadRealFail
		}
		if isAbsentResource(err) {
			logx.Debug("%s 리소스 종류가 없어 건너뜁니다.", resource)
			return nil, nil, loadAbsent
		}
		logx.Warn("%s 조회에 실패했습니다(권한·연결 등). 건너뜁니다: %v", resource, err)
		return nil, nil, loadRealFail
	}
	items, unsup, perr := parseList(out, field)
	if perr != nil {
		logx.Warn("%s 응답을 해석하지 못했습니다. 건너뜁니다: %v", resource, perr)
		return nil, nil, loadRealFail
	}
	return items, unsup, loadOK
}

// isAbsentResource는 서버가 해당 리소스 종류를 제공하지 않는 오류인지 확인합니다.
// 권한·연결 오류 등은 조회 실패로 구분합니다.
func isAbsentResource(err error) bool {
	msg := err.Error()
	for _, s := range []string{
		"doesn't have a resource type",
		"the server could not find the requested resource",
		"could not find the requested resource",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// parseList는 `oc get <resource> -o json` 출력(List)의 미러 규칙을 하나의 목록으로
// 모읍니다. 지원하지 않는 source는 규칙에서 제외하고 별도로 반환해 호출부가
// 경고를 출력할 수 있도록 합니다.
func parseList(jsonText, field string) (items []mapping, unsupported []string, err error) {
	var list struct {
		Items []struct {
			Spec map[string]json.RawMessage `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(jsonText), &list); err != nil {
		return nil, nil, fmt.Errorf("JSON 형식이 아닙니다: %w", err)
	}
	for _, item := range list.Items {
		raw, ok := item.Spec[field]
		if !ok {
			continue
		}
		var entries []struct {
			Source             string   `json:"source"`
			Mirrors            []string `json:"mirrors"`
			MirrorSourcePolicy string   `json:"mirrorSourcePolicy"`
		}
		if uerr := json.Unmarshal(raw, &entries); uerr != nil {
			return nil, nil, fmt.Errorf("%s 필드 형식이 올바르지 않습니다: %w", field, uerr)
		}
		for _, e := range entries {
			src := strings.TrimRight(strings.TrimSpace(e.Source), "/")
			if src == "" || len(e.Mirrors) == 0 {
				continue
			}
			wild, supported := classifySource(src)
			if !supported {
				unsupported = append(unsupported, src)
				continue
			}
			items = append(items, mapping{
				source:   src,
				mirrors:  e.Mirrors,
				noSource: strings.EqualFold(e.MirrorSourcePolicy, "NeverContactSource"),
				wild:     wild,
			})
		}
	}
	return items, unsupported, nil
}

// classifySource는 source가 지원되는 형식인지 확인합니다. 와일드카드가 없는
// 경로 접두사와 `*.suffix` 형태의 호스트 와일드카드를 지원합니다.
// 그 밖에 `*`가 들어간 형식(`reg*.x`, 중간 `*` 등)은 지원하지 않습니다.
func classifySource(src string) (wild, supported bool) {
	if !strings.Contains(src, "*") {
		return false, true
	}
	if strings.HasPrefix(src, "*.") && !strings.Contains(src[2:], "*") && !strings.Contains(src, "/") {
		return true, true
	}
	return false, false
}

// Resolve는 이미지 참조에 적용할 후보 주소와 규칙 매칭 여부를 반환합니다.
// 매칭되는 규칙이 있으면 미러 목록을 반환하고, 정책이 허용하면 원본을 마지막
// 후보로 추가합니다. 매칭이 없으면 원본만 반환합니다. 호출부는 후보를 순서대로
// 시도합니다. 다이제스트 참조에는 IDMS/ICSP, 그 밖의 참조에는 ITMS를 적용합니다.
func (s *Sets) Resolve(ref string) ([]string, bool) {
	if s == nil {
		return []string{ref}, false
	}
	name, suffix, isDigest := split(ref)
	rules := s.tag
	if isDigest {
		rules = s.digest
	}
	segs := strings.Split(name, "/")

	// 가장 구체적인(점수가 높은) 매칭 source를 고릅니다.
	bestScore := -1
	bestSource := ""
	for _, m := range rules {
		if score, ok := matchScore(segs, m); ok && score > bestScore {
			bestScore = score
			bestSource = m.source
		}
	}
	if bestScore < 0 {
		return []string{ref}, false
	}

	// 같은 source의 미러 목록을 모아 mergeOrdered로 병합합니다.
	// 규칙 중 하나라도 NeverContactSource이면 원본은 후보에서 제외합니다.
	var lists [][]string
	allowSource := true
	for _, m := range rules {
		if m.source != bestSource {
			continue
		}
		if m.noSource {
			allowSource = false
		}
		var l []string
		for _, mir := range m.mirrors {
			l = append(l, rewrite(m, segs, mir, suffix))
		}
		lists = append(lists, l)
	}
	cands := mergeOrdered(lists)
	if allowSource {
		cands = appendUnique(cands, ref)
	}
	return cands, true
}

// mergeOrdered는 여러 미러 목록을 상대적 순서를 보존하며 하나로 합칩니다.
// 각 목록의 연속 쌍(x 다음 y)을 순서 제약으로 보고 위상 정렬합니다.
// 순서 제약이 순환해 정렬할 수 없으면 남은 항목을 처음 등장한 순서로 추가합니다.
func mergeOrdered(lists [][]string) []string {
	var order []string // 순서 제약이 없을 때 사용할 최초 등장 순서
	indeg := map[string]int{}
	succ := map[string]map[string]bool{}
	seen := map[string]bool{}
	for _, l := range lists {
		for i, x := range l {
			if !seen[x] {
				seen[x] = true
				order = append(order, x)
				if _, ok := succ[x]; !ok {
					succ[x] = map[string]bool{}
				}
			}
			if i > 0 {
				p := l[i-1]
				if p != x && !succ[p][x] {
					succ[p][x] = true
					indeg[x]++
				}
			}
		}
	}
	used := map[string]bool{}
	var out []string
	for len(out) < len(order) {
		// 아직 사용하지 않았고 indeg가 0인 항목 중 가장 먼저 등장한 것을 선택합니다.
		picked := ""
		for _, x := range order {
			if !used[x] && indeg[x] == 0 {
				picked = x
				break
			}
		}
		if picked == "" {
			// 순환이 있으면 남은 항목을 처음 등장한 순서로 추가합니다.
			for _, x := range order {
				if !used[x] {
					out = append(out, x)
					used[x] = true
				}
			}
			break
		}
		out = append(out, picked)
		used[picked] = true
		for y := range succ[picked] {
			indeg[y]--
		}
	}
	return out
}

// matchScore는 규칙의 구체성 점수와 참조 매칭 여부를 반환합니다.
// 정확한 경로 접두사가 어떤 와일드카드보다도 우선하도록 큰 기준값을
// 더합니다. 정확한 규칙은 경로 요소가 많을수록, 와일드카드는 접미사가
// 길수록 높은 점수를 부여합니다.
func matchScore(segs []string, m mapping) (int, bool) {
	if m.wild {
		if hostMatchesWildcard(segs[0], m.source) {
			return len(m.source), true // `*.team.example.com` > `*.example.com`
		}
		return 0, false
	}
	srcSegs := strings.Split(m.source, "/")
	if len(srcSegs) > len(segs) || !segPrefix(segs, srcSegs) {
		return 0, false
	}
	return exactBase + len(srcSegs), true
}

// exactBase는 정확한 매칭이 항상 와일드카드보다 높은 점수를 갖도록 하는
// 기준값으로, 일반적인 source 길이보다 충분히 큰 값을 사용합니다.
const exactBase = 1 << 20

// rewrite는 매칭된 규칙으로 참조를 미러 주소로 바꿉니다.
func rewrite(m mapping, segs []string, mirror, suffix string) string {
	if m.wild {
		// 와일드카드는 호스트 라벨까지만 매칭하고, 그 뒤(원본 호스트의 :포트,
		// 경로, 접미사)는 그대로 mirror에 이어 붙입니다. 공식 containers/image의
		// `Location + ref[호스트길이:]` 연결과 동일하게 포트를 보존합니다.
		// 예: *.example.com / mirror.net, reg.example.com:5000/r@sha
		//     → mirror.net:5000/r@sha
		port := ""
		if i := strings.LastIndexByte(segs[0], ':'); i >= 0 {
			port = segs[0][i:] // ":5000"
		}
		rest := segs[1:]
		if len(rest) == 0 {
			return mirror + port + suffix
		}
		return mirror + port + "/" + strings.Join(rest, "/") + suffix
	}
	name := strings.Join(segs, "/")
	return mirror + strings.TrimPrefix(name, m.source) + suffix
}

// hostMatchesWildcard는 host가 `*.suffix` 와일드카드에 매칭되는지 확인합니다.
// host의 :포트는 매칭에서 제외합니다(reg.example.com:5000 → reg.example.com).
func hostMatchesWildcard(host, wildcard string) bool {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	suffix := wildcard[1:] // "*.redhat.io" -> ".redhat.io"
	return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
}

// split은 참조를 이름 부분과 접미사(@다이제스트 또는 :태그)로 나눕니다.
// 호스트의 :포트를 태그로 오인하지 않도록 마지막 / 뒤의 :만 태그로 봅니다.
func split(ref string) (name, suffix string, isDigest bool) {
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		return ref[:at], ref[at:], true
	}
	lastSlash := strings.LastIndexByte(ref, '/')
	if colon := strings.LastIndexByte(ref, ':'); colon > lastSlash {
		return ref[:colon], ref[colon:], false
	}
	return ref, "", false
}

// segPrefix는 src가 name의 경로 요소 접두사인지 확인합니다.
func segPrefix(name, src []string) bool {
	for i := range src {
		if name[i] != src[i] {
			return false
		}
	}
	return true
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
