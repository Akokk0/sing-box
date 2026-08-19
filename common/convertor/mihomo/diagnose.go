package mihomo

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// looksLikeClashSubscription 判断这份内容到底是不是一份 clash 订阅。
//
// 机场按 User-Agent 决定返回什么：认出 clash 就给 clash yaml，认不出常给 base64 那种
// 订阅，也可能干脆是登录页或限流页。把那些东西交给 YAML 解析器，用户只会收到一句词法
// 错误加一个行号，对着它完全无从下手——而真正该做的是换个 user_agent。
func looksLikeClashSubscription(content []byte) bool {
	for line := range bytes.Lines(content) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("proxies:")) {
			return true
		}
	}
	return false
}

var yamlLinePattern = regexp.MustCompile(`line (\d+):`)

// excerptAround 把出问题的那几行摘出来附在错误后面。
//
// 只报行号是不够的：用户看不到那一行长什么样，而订阅是机场生成的，他也改不了。摘出来
// 才能一眼看出是缩进错了、还是名字里带了个没转义的冒号。
//
// 摘的是一小段窗口而不是单独一行，因为 yaml.v3 的行号常常指向坏行的**前一行**——缩进
// 写错时它指的是上一行的末尾。
func excerptAround(content []byte, errMessage string) string {
	match := yamlLinePattern.FindStringSubmatch(errMessage)
	if match == nil {
		return ""
	}
	reported, err := strconv.Atoi(match[1])
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if reported < 1 || reported > len(lines) {
		return ""
	}
	from := max(reported-1, 1)
	to := min(reported+2, len(lines))
	var builder strings.Builder
	for number := from; number <= to; number++ {
		builder.WriteString("\n  ")
		builder.WriteString(strconv.Itoa(number))
		builder.WriteString(" | ")
		builder.WriteString(redactSecrets(lines[number-1]))
	}
	return builder.String()
}

// secretValuePattern 匹配「密钥类字段: 它的值」。值一直取到 , 或 } 或行尾——flow 风格
// （{name: x, password: y}）和块风格都是这么断的。
var secretValuePattern = regexp.MustCompile(`(?i)\b(password|passwd|uuid|psk|token|secret|auth[-_]?str|private[-_]?key)(\s*:\s*)[^,}\n]*`)

// redactSecrets 抹掉一行里的密码之类的值。
//
// 这一行是要进日志的，而订阅里每个节点都带着机场给的密码。诊断信息不能是一次凭证泄露：
// 日志会被翻出来看、会被贴进 issue、会被发给别人。
func redactSecrets(line string) string {
	redacted := secretValuePattern.ReplaceAllString(line, "${1}${2}[redacted]")
	const limit = 200
	if len(redacted) > limit {
		return redacted[:limit] + "…"
	}
	return redacted
}

// proxiesBlock 从一份 mihomo 配置里把 proxies 段整块切出来，切不到则返回 nil。
//
// 订阅是一份完整的 mihomo 配置，而我们只要 proxies。其余段落由机场生成，坏掉是常事——
// 现场遇到过一个空的 hosts 段（两行只有个冒号），它让整份订阅解析失败，节点一个都拿不到。
// 为一个我们从不读的段落赔上全部节点，等于让路由器断网。
//
// 按文本切而不是按 YAML 切，正是因为这时候整份文档已经解析不了了。clash 订阅的结构很规整：
// 顶层键顶格写，段落内容缩进。切到下一个顶格键为止就够了。
func proxiesBlock(content []byte) []byte {
	lines := bytes.Split(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")), []byte("\n"))
	start := -1
	for index, line := range lines {
		if bytes.HasPrefix(line, []byte("proxies:")) {
			start = index
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if isTopLevelKey(lines[index]) {
			end = index
			break
		}
	}
	return bytes.Join(lines[start:end], []byte("\n"))
}

// isTopLevelKey 判断这一行是不是又一个顶格的段落开头。
//
// 空行、注释、以及任何缩进了的行都属于当前段落。顶格的 `- ` 也是：那是顶层序列的一项，
// clash 配置里不会出现，但真出现了也不该被当成新段落的开头。
func isTopLevelKey(line []byte) bool {
	trimmed := bytes.TrimRight(line, " \t")
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '#' || trimmed[0] == '-' {
		return false
	}
	return bytes.Contains(trimmed, []byte(":"))
}
