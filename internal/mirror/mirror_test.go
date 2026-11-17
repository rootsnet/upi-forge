package mirror

import (
	"reflect"
	"testing"
)

// 가장 구체적인 source의 미러를 후보로 반환하고, 정책이 허용하면
// 원본을 마지막 후보로 추가하는지 확인합니다.
func TestResolveCandidates(t *testing.T) {
	s := &Sets{
		digest: []mapping{
			// 짧은 접두사를 먼저 배치해도 더 긴 art-dev 규칙이 선택되는지 확인합니다.
			{source: "quay.io", mirrors: []string{"m.local/quay"}},
			{source: "quay.io/openshift-release-dev/ocp-v4.0-art-dev",
				mirrors: []string{"m1.local/release", "m2.local/release"}},
			// 같은 source를 가진 다른 규칙의 미러도 병합되는지 확인합니다.
			{source: "quay.io/openshift-release-dev/ocp-v4.0-art-dev",
				mirrors: []string{"m3.local/release"}},
			{source: "registry.redhat.io", mirrors: []string{"m.local/rh"},
				noSource: true},
			{source: "*.example.io", mirrors: []string{"m.local/wild"}, wild: true},
		},
		tag: []mapping{
			{source: "quay.io/openshift-release-dev/ocp-release",
				mirrors: []string{"m.local/release-images"}},
		},
	}

	cases := []struct {
		name string
		in   string
		want []string
		ok   bool
	}{
		{
			name: "복수 미러 병합 + 같은 source 병합 + 원본 후보",
			in:   "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:abc",
			want: []string{
				"m1.local/release@sha256:abc",
				"m2.local/release@sha256:abc",
				"m3.local/release@sha256:abc",
				"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:abc",
			},
			ok: true,
		},
		{
			name: "NeverContactSource이면 원본을 후보에 넣지 않음",
			in:   "registry.redhat.io/openshift4/ose-foo@sha256:def",
			want: []string{"m.local/rh/openshift4/ose-foo@sha256:def"},
			ok:   true,
		},
		{
			name: "짧은 quay 규칙(최장 접두사에서 밀림)",
			in:   "quay.io/other/thing@sha256:111",
			want: []string{"m.local/quay/other/thing@sha256:111", "quay.io/other/thing@sha256:111"},
			ok:   true,
		},
		{
			name: "태그 참조는 ITMS 규칙 적용(호스트 포트 오인 안 함)",
			in:   "quay.io/openshift-release-dev/ocp-release:4.22.5-x86_64",
			want: []string{
				"m.local/release-images:4.22.5-x86_64",
				"quay.io/openshift-release-dev/ocp-release:4.22.5-x86_64",
			},
			ok: true,
		},
		{
			name: "호스트 와일드카드 *.example.io",
			in:   "reg.example.io/product/repo@sha256:777",
			want: []string{"m.local/wild/product/repo@sha256:777", "reg.example.io/product/repo@sha256:777"},
			ok:   true,
		},
		{
			name: "매칭 없음: 원본 그대로",
			in:   "gcr.io/foo/bar@sha256:333",
			want: []string{"gcr.io/foo/bar@sha256:333"},
			ok:   false,
		},
		{
			name: "부분 요소는 접두사로 보지 않음",
			in:   "quay.iox/evil@sha256:444",
			want: []string{"quay.iox/evil@sha256:444"},
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.Resolve(tc.in)
			if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve(%q) = %v,%t; want %v,%t", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// 정확한 호스트 규칙이 같은 호스트에 매칭되는 와일드카드보다 우선하는지 확인합니다.
func TestExactBeatsWildcard(t *testing.T) {
	s := &Sets{digest: []mapping{
		{source: "*.redhat.io", mirrors: []string{"m.local/wild"}, wild: true},
		{source: "registry.redhat.io", mirrors: []string{"m.local/exact"}},
	}}
	got, ok := s.Resolve("registry.redhat.io/a/b@sha256:1")
	want := []string{"m.local/exact/a/b@sha256:1", "registry.redhat.io/a/b@sha256:1"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("정확한 규칙 우선: got %v,%t want %v", got, ok, want)
	}
}

func TestSplit(t *testing.T) {
	cases := []struct {
		in       string
		name     string
		suffix   string
		isDigest bool
	}{
		{"quay.io/a/b@sha256:x", "quay.io/a/b", "@sha256:x", true},
		{"quay.io/a/b:tag", "quay.io/a/b", ":tag", false},
		{"mirror:8443/a/b:tag", "mirror:8443/a/b", ":tag", false},
		{"mirror:8443/a/b", "mirror:8443/a/b", "", false},
		{"quay.io/a/b", "quay.io/a/b", "", false},
	}
	for _, tc := range cases {
		name, suffix, isDigest := split(tc.in)
		if name != tc.name || suffix != tc.suffix || isDigest != tc.isDigest {
			t.Errorf("split(%q) = %q,%q,%t; want %q,%q,%t",
				tc.in, name, suffix, isDigest, tc.name, tc.suffix, tc.isDigest)
		}
	}
}

// parseList가 mirrorSourcePolicy를 읽고, 미지원 와일드카드를 규칙에서 제외해
// 별도로 반환하는지 확인합니다. source 끝의 슬래시를 제거하는지도 확인합니다.
func TestParseList(t *testing.T) {
	jsonText := `{"items":[
      {"spec":{"imageDigestMirrors":[
        {"source":"registry.redhat.io/","mirrors":["m.local/rh"],"mirrorSourcePolicy":"NeverContactSource"},
        {"source":"*.example.com","mirrors":["m.local/w"]},
        {"source":"reg*.bad.com","mirrors":["m.local/bad"]},
        {"source":"has/*/star","mirrors":["m.local/mid"]},
        {"source":"no-mirror.io","mirrors":[]}
      ]}}
    ]}`
	got, unsupported, err := parseList(jsonText, "imageDigestMirrors")
	if err != nil {
		t.Fatalf("parseList 오류: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("규칙 수: got %d want 2 (%+v)", len(got), got)
	}
	if got[0].source != "registry.redhat.io" || !got[0].noSource {
		t.Errorf("source 정규화/정책: %+v", got[0])
	}
	if !got[1].wild || got[1].source != "*.example.com" {
		t.Errorf("와일드카드 규칙: %+v", got[1])
	}
	if len(unsupported) != 2 {
		t.Errorf("미지원 source 목록: %v", unsupported)
	}
}

func TestClassifySource(t *testing.T) {
	cases := []struct {
		src       string
		wild      bool
		supported bool
	}{
		{"quay.io/a/b", false, true},
		{"*.redhat.io", true, true},
		{"reg*.redhat.io", false, false},
		{"*.redhat.io/path", false, false},
		{"a/*/b", false, false},
	}
	for _, tc := range cases {
		w, s := classifySource(tc.src)
		if w != tc.wild || s != tc.supported {
			t.Errorf("classifySource(%q) = %t,%t; want %t,%t", tc.src, w, s, tc.wild, tc.supported)
		}
	}
}

// 겹치는 와일드카드 중 접미사가 더 긴 규칙이 우선하는지 확인합니다.
func TestWildcardSpecificity(t *testing.T) {
	s := &Sets{digest: []mapping{
		// 접미사가 짧은 규칙을 먼저 배치해 조회 순서와 우선순위를 구분합니다.
		{source: "*.example.com", mirrors: []string{"m.local/general"}, wild: true},
		{source: "*.team.example.com", mirrors: []string{"m.local/team"}, wild: true, noSource: true},
	}}
	got, ok := s.Resolve("reg.team.example.com/repo@sha256:1")
	want := []string{"m.local/team/repo@sha256:1"} // 전용 미러 + NeverContactSource(원본 없음)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("구체적 와일드카드 우선: got %v,%t want %v", got, ok, want)
	}
}

// 포트가 붙은 레지스트리도 와일드카드에 매칭되고, 치환 결과에 원본 포트가
// 유지되는지 확인합니다. 호스트만 바꾸고 포트·경로·다이제스트는 그대로 둡니다.
func TestWildcardPortMatch(t *testing.T) {
	s := &Sets{digest: []mapping{
		{source: "*.example.com", mirrors: []string{"mirror.example.net"}, wild: true},
	}}
	got, ok := s.Resolve("reg.example.com:5000/repo@sha256:1")
	want := []string{"mirror.example.net:5000/repo@sha256:1", "reg.example.com:5000/repo@sha256:1"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("포트 보존 와일드카드 치환: got %v,%t want %v", got, ok, want)
	}
}

// mergeOrdered가 미러의 상대적 순서를 보존하고, 순환 시 최초 등장 순서를
// 사용하는지 확인합니다.
func TestMergeOrdered(t *testing.T) {
	cases := []struct {
		in   [][]string
		want []string
	}{
		{[][]string{{"B", "C"}, {"A", "B"}}, []string{"A", "B", "C"}},
		{[][]string{{"A", "B"}, {"B", "C"}}, []string{"A", "B", "C"}},
		{[][]string{{"A", "B"}, {"C", "D"}}, []string{"A", "B", "C", "D"}},
		{[][]string{{"A", "B"}, {"B", "A"}}, []string{"A", "B"}}, // 순환: 처음 본 순서
	}
	for _, tc := range cases {
		if got := mergeOrdered(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("mergeOrdered(%v) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

// 같은 source의 미러가 여러 규칙에 나뉘어 있어도 순서를 보존해 병합하는지 확인합니다.
func TestResolveMergesPreservingOrder(t *testing.T) {
	s := &Sets{digest: []mapping{
		{source: "quay.io/x", mirrors: []string{"B.local/x", "C.local/x"}},
		{source: "quay.io/x", mirrors: []string{"A.local/x", "B.local/x"}},
	}}
	got, ok := s.Resolve("quay.io/x/repo@sha256:1")
	want := []string{
		"A.local/x/repo@sha256:1",
		"B.local/x/repo@sha256:1",
		"C.local/x/repo@sha256:1",
		"quay.io/x/repo@sha256:1",
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("순서 보존 병합: got %v want %v", got, want)
	}
}
