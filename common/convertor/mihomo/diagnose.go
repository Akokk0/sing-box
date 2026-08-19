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
