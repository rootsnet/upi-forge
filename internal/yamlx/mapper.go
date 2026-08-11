package yamlx

import (
	"fmt"
	"strconv"
	"strings"
)

// Mapper는 노드 트리에서 값을 꺼내며 사용한 키를 기록합니다.
//
// 리플렉션 기반 언마셜 대신 명시적 접근자를 쓰는 이유는 두 가지입니다.
//
//  1. 설정 파일에 오타 난 키가 있으면 Finish에서 확실히 잡아냅니다.
//  2. 어떤 설정 키가 코드의 어느 필드로 가는지 한눈에 보입니다.
type Mapper struct {
	node   *Node
	path   string
	used   map[string]bool
	errors *[]error
}

// NewMapper는 최상위 매핑에 대한 Mapper를 만듭니다.
func NewMapper(node *Node) *Mapper {
	errs := &[]error{}
	m := &Mapper{node: node, path: "", used: map[string]bool{}, errors: errs}
	if node != nil && node.Kind != KindMapping {
		m.fail("최상위는 매핑(key: value)이어야 합니다")
		m.node = nil
	}
	return m
}

func (m *Mapper) fail(format string, args ...any) {
	*m.errors = append(*m.errors, fmt.Errorf(format, args...))
}

func (m *Mapper) key(name string) string {
	if m.path == "" {
		return name
	}
	return m.path + "." + name
}

func (m *Mapper) child(name string) *Node {
	if m.node == nil {
		return nil
	}
	m.used[m.path+"\x00"+name] = true
	node, ok := m.node.Fields[name]
	if !ok {
		return nil
	}
	return node
}

// Section은 하위 매핑에 대한 Mapper를 반환합니다.
// 키가 없으면 빈 Mapper를 반환하므로 호출 측에서 nil 검사를 하지 않아도 됩니다.
func (m *Mapper) Section(name string) *Mapper {
	node := m.child(name)
	sub := &Mapper{path: m.key(name), used: m.used, errors: m.errors}
	switch {
	case node == nil:
		return sub
	case node.Kind == KindMapping:
		sub.node = node
	case node.Kind == KindScalar && node.Value == "":
		// "key:" 만 있고 내용이 없는 경우는 빈 섹션으로 봅니다.
	default:
		m.fail("%s: 매핑이어야 합니다 (%d행)", sub.path, node.Line)
	}
	return sub
}

// Node는 키를 소비하고 원본 노드를 그대로 반환합니다.
// 접근자로 다루기 어려운 구조를 직접 읽어야 할 때 사용합니다.
func (m *Mapper) Node(name string) *Node { return m.child(name) }

// Has는 키가 존재하는지 확인합니다.
func (m *Mapper) Has(name string) bool {
	if m.node == nil {
		return false
	}
	_, ok := m.node.Fields[name]
	return ok
}

// String은 문자열 값을 읽습니다. 키가 없으면 dst를 바꾸지 않습니다.
func (m *Mapper) String(name string, dst *string) {
	node := m.child(name)
	if node == nil {
		return
	}
	if node.Kind != KindScalar {
		m.fail("%s: 문자열이어야 합니다 (%d행)", m.key(name), node.Line)
		return
	}
	*dst = node.Value
}

// Int는 정수 값을 읽습니다.
func (m *Mapper) Int(name string, dst *int) {
	node := m.child(name)
	if node == nil {
		return
	}
	if node.Kind != KindScalar {
		m.fail("%s: 정수여야 합니다 (%d행)", m.key(name), node.Line)
		return
	}
	if strings.TrimSpace(node.Value) == "" {
		return
	}
	value, err := strconv.Atoi(strings.TrimSpace(node.Value))
	if err != nil {
		m.fail("%s: 정수여야 합니다: %q (%d행)", m.key(name), node.Value, node.Line)
		return
	}
	*dst = value
}

// Bool은 불리언 값을 읽습니다. true/false, yes/no, on/off를 허용합니다.
func (m *Mapper) Bool(name string, dst *bool) {
	node := m.child(name)
	if node == nil {
		return
	}
	if node.Kind != KindScalar {
		m.fail("%s: true 또는 false여야 합니다 (%d행)", m.key(name), node.Line)
		return
	}
	raw := strings.TrimSpace(strings.ToLower(node.Value))
	if raw == "" {
		return
	}
	switch raw {
	case "true", "yes", "on":
		*dst = true
	case "false", "no", "off":
		*dst = false
	default:
		m.fail("%s: true 또는 false여야 합니다: %q (%d행)", m.key(name), node.Value, node.Line)
	}
}

// Strings는 문자열 목록을 읽습니다. 블록 시퀀스와 한 줄 목록을 모두 허용합니다.
func (m *Mapper) Strings(name string, dst *[]string) {
	node := m.child(name)
	if node == nil {
		return
	}
	switch node.Kind {
	case KindSequence:
		out := make([]string, 0, len(node.Items))
		for _, item := range node.Items {
			if item.Kind != KindScalar {
				m.fail("%s: 문자열 목록이어야 합니다 (%d행)", m.key(name), item.Line)
				return
			}
			out = append(out, item.Value)
		}
		*dst = out
	case KindScalar:
		if strings.TrimSpace(node.Value) == "" {
			*dst = nil
			return
		}
		m.fail("%s: 문자열 목록이어야 합니다 (%d행)", m.key(name), node.Line)
	default:
		m.fail("%s: 문자열 목록이어야 합니다 (%d행)", m.key(name), node.Line)
	}
}

// Finish는 사용하지 않은 키(오타 가능성)를 오류로 모아 반환합니다.
// 최상위 Mapper에서 한 번만 호출합니다.
func (m *Mapper) Finish() error {
	m.checkUnknown()
	errs := *m.errors
	if len(errs) == 0 {
		return nil
	}
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, "  - "+err.Error())
	}
	return fmt.Errorf("설정을 해석하지 못했습니다:\n%s", strings.Join(messages, "\n"))
}

func (m *Mapper) checkUnknown() {
	if m.node == nil {
		return
	}
	for _, key := range m.node.Keys {
		if !m.used[m.path+"\x00"+key] {
			m.fail("%s: 알 수 없는 설정 키입니다 (%d행)", m.key(key), m.node.Fields[key].Line)
		}
	}
	for _, key := range m.node.Keys {
		child := m.node.Fields[key]
		if child.Kind != KindMapping {
			continue
		}
		sub := &Mapper{node: child, path: m.key(key), used: m.used, errors: m.errors}
		sub.checkUnknown()
	}
}
