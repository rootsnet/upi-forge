package yamlx

import (
	"fmt"
	"strconv"
	"strings"
)

// Quote는 값을 항상 큰따옴표 문자열로 출력합니다.
// 숫자처럼 보이는 MAC 주소나 버전 문자열이 다른 타입으로 해석되는 것을 막고,
// 장비가 보낸 제어 문자나 비문자가 YAML 문서를 깨뜨리지 않게 합니다. 탭과
// 줄바꿈은 짧은 이스케이프(\t, \n)를 사용하고, 나머지 C0 문자, DEL/C1 문자,
// U+FFFE/U+FFFF는 \uXXXX로 출력합니다. YAML 1.2가 허용하는 U+0085(NEL)도
// 개행류 문자이므로 문서 구조에 영향을 주지 않도록 이스케이프합니다.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || (r >= 0x7F && r <= 0x9F) || r == 0xFFFE || r == 0xFFFF {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Number는 정수 포인터를 YAML 스칼라로 출력합니다. nil이면 null입니다.
func Number(v *int) string {
	if v == nil {
		return "null"
	}
	return strconv.Itoa(*v)
}
