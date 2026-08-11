// Package yamlx는 설정 파일에 필요한 YAML 부분집합만 다루는 파서입니다.
//
// 왜 외부 라이브러리를 쓰지 않는가:
//
//	upi-forge는 단절망(disconnected) bastion에서 빌드하고 실행하는 경우가 많습니다.
//	외부 모듈에 의존하면 그 환경에서 `go build`가 바로 되지 않습니다.
//	표준 라이브러리만 사용하면 Go만 설치되어 있으면 언제나 빌드됩니다.
//
// 지원하는 문법(설정 파일에 필요한 범위):
//
//	key: value            스칼라
//	key:                  중첩 블록(맵 또는 시퀀스)
//	  nested: value
//	list:                 블록 시퀀스 (키보다 깊은 들여쓰기)
//	  - item
//	  - key: value        시퀀스 안의 맵
//	    other: value
//	list2:                블록 시퀀스 (키와 같은 들여쓰기도 표준 YAML이므로 허용)
//	- item
//	flow: [a, b, c]       한 줄 시퀀스
//	"quoted", 'quoted'    따옴표 문자열
//	# 주석                주석
//
// 지원하지 않는 문법(앵커, 별칭, 여러 문서, 블록 스칼라 |, > 등)은 조용히
// 무시하지 않고 오류로 알립니다. 설정 파일이 의도와 다르게 해석되는 것보다
// 즉시 실패하는 편이 안전하기 때문입니다.
package yamlx

import (
	"fmt"
	"strings"
)

// Kind는 노드 종류입니다.
type Kind int

const (
	// KindScalar는 문자열, 숫자, 불리언 같은 단일 값입니다.
	KindScalar Kind = iota
	// KindMapping은 key: value 모음입니다.
	KindMapping
	// KindSequence는 항목 목록입니다.
	KindSequence
)

// Node는 파싱한 YAML 트리의 한 노드입니다.
type Node struct {
	Kind  Kind
	Line  int
	Value string // KindScalar일 때의 값

	Keys   []string         // KindMapping의 키 등장 순서
	Fields map[string]*Node // KindMapping의 키-값
	Items  []*Node          // KindSequence의 항목
}

// Parse는 YAML 문서 하나를 노드 트리로 읽습니다.
// 빈 문서는 빈 매핑 노드를 반환합니다.
func Parse(data []byte) (*Node, error) {
	lines, err := scanLines(string(data))
	if err != nil {
		return nil, err
	}
	p := &parser{lines: lines}
	if p.done() {
		return &Node{Kind: KindMapping, Fields: map[string]*Node{}}, nil
	}
	node, err := p.parseNode(p.current().indent)
	if err != nil {
		return nil, err
	}
	if !p.done() {
		return nil, fmt.Errorf("%d행: 들여쓰기가 맞지 않습니다: %q", p.current().number, p.current().raw)
	}
	return node, nil
}

// line은 의미 있는 한 줄입니다.
type line struct {
	number  int    // 원본 파일의 행 번호(1부터)
	indent  int    // 앞쪽 공백 수
	content string // 주석과 앞뒤 공백을 제거한 내용
	raw     string // 원본 줄(오류 메시지용)
}

// scanLines는 주석과 빈 줄을 제거하고 들여쓰기를 계산합니다.
func scanLines(text string) ([]line, error) {
	var out []line
	for i, raw := range strings.Split(text, "\n") {
		number := i + 1
		trimmedRight := strings.TrimRight(raw, "\r")

		// 탭 들여쓰기는 YAML에서 허용되지 않습니다.
		// 눈에 보이지 않는 오류를 만들기 쉬우므로 즉시 중단합니다.
		if leading := len(trimmedRight) - len(strings.TrimLeft(trimmedRight, " \t")); leading > 0 {
			if strings.Contains(trimmedRight[:leading], "\t") {
				return nil, fmt.Errorf("%d행: 들여쓰기에 탭을 사용할 수 없습니다", number)
			}
		}

		content := stripComment(trimmedRight)
		if strings.TrimSpace(content) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(content), "---") ||
			strings.HasPrefix(strings.TrimSpace(content), "...") {
			return nil, fmt.Errorf("%d행: 여러 YAML 문서(--- / ...)는 지원하지 않습니다", number)
		}

		indent := len(content) - len(strings.TrimLeft(content, " "))
		out = append(out, line{
			number:  number,
			indent:  indent,
			content: strings.TrimRight(strings.TrimLeft(content, " "), " "),
			raw:     trimmedRight,
		})
	}
	return out, nil
}

// stripComment는 따옴표 밖의 # 이후를 제거합니다.
func stripComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if inSingle || inDouble {
				continue
			}
			// 줄 시작이거나 앞이 공백일 때만 주석으로 봅니다.
			if i == 0 || s[i-1] == ' ' {
				return s[:i]
			}
		}
	}
	return s
}

type parser struct {
	lines []line
	pos   int
}

func (p *parser) done() bool      { return p.pos >= len(p.lines) }
func (p *parser) current() line   { return p.lines[p.pos] }
func (p *parser) advance()        { p.pos++ }
func (p *parser) peekIndent() int { return p.lines[p.pos].indent }

// parseNode는 주어진 들여쓰기 수준의 매핑 또는 시퀀스를 읽습니다.
func (p *parser) parseNode(indent int) (*Node, error) {
	if p.done() {
		return &Node{Kind: KindMapping, Fields: map[string]*Node{}}, nil
	}
	if isSequenceItem(p.current().content) {
		return p.parseSequence(indent)
	}
	return p.parseMapping(indent)
}

func isSequenceItem(content string) bool {
	return content == "-" || strings.HasPrefix(content, "- ")
}

func (p *parser) parseMapping(indent int) (*Node, error) {
	node := &Node{
		Kind:   KindMapping,
		Line:   p.current().number,
		Fields: map[string]*Node{},
	}
	for !p.done() {
		cur := p.current()
		if cur.indent < indent {
			break
		}
		if cur.indent > indent {
			return nil, fmt.Errorf("%d행: 들여쓰기가 맞지 않습니다: %q", cur.number, cur.raw)
		}
		if isSequenceItem(cur.content) {
			return nil, fmt.Errorf("%d행: 매핑 안에서 시퀀스 항목을 만났습니다: %q", cur.number, cur.raw)
		}

		key, rest, err := splitKey(cur)
		if err != nil {
			return nil, err
		}
		if _, dup := node.Fields[key]; dup {
			return nil, fmt.Errorf("%d행: 키가 중복되었습니다: %s", cur.number, key)
		}
		p.advance()

		var value *Node
		switch {
		case rest != "":
			value, err = parseInlineValue(cur.number, rest)
			if err != nil {
				return nil, err
			}
		case !p.done() && p.peekIndent() > indent:
			value, err = p.parseNode(p.peekIndent())
			if err != nil {
				return nil, err
			}
		case !p.done() && p.peekIndent() == indent && isSequenceItem(p.current().content):
			// 표준 YAML은 목록을 키와 같은 들여쓰기로 쓸 수 있습니다.
			//   dns:
			//   - 192.0.2.10
			// 시퀀스는 항목이 아닌 줄을 만나면 멈추므로,
			// 같은 들여쓰기의 다음 키("gateway: ...")에서 매핑 파싱이 이어집니다.
			value, err = p.parseSequence(indent)
			if err != nil {
				return nil, err
			}
		default:
			// 값이 없는 키는 빈 스칼라로 다룹니다.
			value = &Node{Kind: KindScalar, Line: cur.number}
		}

		node.Keys = append(node.Keys, key)
		node.Fields[key] = value
	}
	return node, nil
}

func (p *parser) parseSequence(indent int) (*Node, error) {
	node := &Node{Kind: KindSequence, Line: p.current().number}
	for !p.done() {
		cur := p.current()
		if cur.indent < indent {
			break
		}
		if cur.indent > indent {
			return nil, fmt.Errorf("%d행: 들여쓰기가 맞지 않습니다: %q", cur.number, cur.raw)
		}
		if !isSequenceItem(cur.content) {
			break
		}

		rest := strings.TrimSpace(strings.TrimPrefix(cur.content, "-"))
		if rest == "" {
			p.advance()
			if p.done() || p.peekIndent() <= indent {
				node.Items = append(node.Items, &Node{Kind: KindScalar, Line: cur.number})
				continue
			}
			child, err := p.parseNode(p.peekIndent())
			if err != nil {
				return nil, err
			}
			node.Items = append(node.Items, child)
			continue
		}

		// "- key: value" 형태는 대시를 공백으로 바꾼 뒤 매핑으로 다시 읽습니다.
		// 이렇게 하면 이어지는 "  other: value" 줄이 같은 매핑으로 묶입니다.
		if _, _, err := splitKey(line{number: cur.number, content: rest, raw: cur.raw}); err == nil {
			contentCol := strings.Index(cur.raw, "-") + 1
			for contentCol < len(cur.raw) && cur.raw[contentCol] == ' ' {
				contentCol++
			}
			p.lines[p.pos] = line{
				number:  cur.number,
				indent:  contentCol,
				content: rest,
				raw:     cur.raw,
			}
			child, err := p.parseMapping(contentCol)
			if err != nil {
				return nil, err
			}
			node.Items = append(node.Items, child)
			continue
		}

		value, err := parseInlineValue(cur.number, rest)
		if err != nil {
			return nil, err
		}
		node.Items = append(node.Items, value)
		p.advance()
	}
	return node, nil
}

// splitKey는 "key: rest" 형태를 나눕니다.
func splitKey(l line) (key, rest string, err error) {
	content := l.content
	inSingle, inDouble := false, false
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if inSingle || inDouble {
				continue
			}
			if i+1 < len(content) && content[i+1] != ' ' {
				continue // "http://..." 처럼 값 안의 콜론
			}
			key = strings.TrimSpace(content[:i])
			rest = strings.TrimSpace(content[i+1:])
			if key == "" {
				return "", "", fmt.Errorf("%d행: 키가 비어 있습니다: %q", l.number, l.raw)
			}
			key = unquote(key)
			return key, rest, nil
		}
	}
	return "", "", fmt.Errorf("%d행: key: value 형식이 아닙니다: %q", l.number, l.raw)
}

// parseInlineValue는 한 줄 값(스칼라 또는 flow 시퀀스)을 읽습니다.
func parseInlineValue(lineNo int, rest string) (*Node, error) {
	if strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, ">") {
		return nil, fmt.Errorf("%d행: 블록 스칼라(| , >)는 지원하지 않습니다", lineNo)
	}
	if strings.HasPrefix(rest, "&") || strings.HasPrefix(rest, "*") {
		return nil, fmt.Errorf("%d행: 앵커와 별칭은 지원하지 않습니다", lineNo)
	}
	if strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("%d행: 한 줄 매핑({...})은 지원하지 않습니다", lineNo)
	}
	if strings.HasPrefix(rest, "[") {
		if !strings.HasSuffix(rest, "]") {
			return nil, fmt.Errorf("%d행: 닫히지 않은 목록입니다: %q", lineNo, rest)
		}
		node := &Node{Kind: KindSequence, Line: lineNo}
		inner := strings.TrimSpace(rest[1 : len(rest)-1])
		if inner == "" {
			return node, nil
		}
		for _, part := range splitFlow(inner) {
			node.Items = append(node.Items, &Node{
				Kind:  KindScalar,
				Line:  lineNo,
				Value: unquote(strings.TrimSpace(part)),
			})
		}
		return node, nil
	}
	return &Node{Kind: KindScalar, Line: lineNo, Value: unquote(rest)}, nil
}

// splitFlow는 따옴표를 존중하며 쉼표로 나눕니다.
func splitFlow(s string) []string {
	var out []string
	var buf strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ',':
			if !inSingle && !inDouble {
				out = append(out, buf.String())
				buf.Reset()
				continue
			}
		}
		buf.WriteByte(c)
	}
	out = append(out, buf.String())
	return out
}

// unquote는 따옴표를 제거하고 기본 이스케이프를 처리합니다.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			inner := s[1 : len(s)-1]
			replacer := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\t`, "\t")
			return replacer.Replace(inner)
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	return s
}
