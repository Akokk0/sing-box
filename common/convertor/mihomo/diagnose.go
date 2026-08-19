package mihomo

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

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
	// 按字符截断而不是按字节：节点名几乎都是 emoji 和中文，砍在字节上会切出半个字符，
	// 整条日志跟着变成非法 UTF-8。
	const limit = 200
	if utf8.RuneCountInString(redacted) > limit {
		return string([]rune(redacted)[:limit]) + "…"
	}
	return redacted
}

// proxiesBlock 从一份 mihomo 配置里把 proxies 段整块切出来，并报告文档里有几个 proxies 段。
//
// 段数是调用方分辨三种局面的依据，一次扫描就够：0 个说明拿到的根本不是 clash 订阅
// （机场没认出 User-Agent，给了 base64 订阅或一张登录页）；1 个才切；多于 1 个不切——
// 切出来的那一份看着完整，实际只是其中一半，少掉的那些会被当成「机场撤掉了这些节点」
// 把出站摘掉，组瞬间缩水。整份失败反而什么都不动，上一次的好状态原样保住。
//
// 订阅是一份完整的 mihomo 配置，而我们只要 proxies。其余段落由机场生成，坏掉是常事——
// 现场遇到过一个空的 hosts 段（两行只有个冒号），它让整份订阅解析失败，节点一个都拿不到。
// 为一个我们从不读的段落赔上全部节点，等于让路由器断网。
//
// 按文本切而不是按 YAML 切，正是因为这时候整份文档已经解析不了了：yaml.v3 的语法错误是
// panic 出来的，整个解码栈被展开，没有半棵节点树可供抢救。clash 订阅的结构很规整：
// 顶层键顶格写，段落内容缩进。切到下一个顶格键为止就够了。
func proxiesBlock(content []byte) (block []byte, sections int) {
	lines := bytes.Split(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")), []byte("\n"))
	start := -1
	for index, line := range lines {
		if bytes.HasPrefix(line, []byte("proxies:")) {
			sections++
			if start < 0 {
				start = index
			}
		}
	}
	if sections != 1 {
		return nil, sections
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if isTopLevelKey(lines[index]) {
			end = index
			break
		}
	}
	return bytes.Join(lines[start:end], []byte("\n")), sections
}

// isTopLevelKey 判断这一行是不是又一个顶格的段落开头。
//
// 空行、注释、以及任何缩进了的行都属于当前段落。顶格的 `- ` 也是：那是顶层序列的一项，
// clash 配置里不会出现，但真出现了也不该被当成新段落的开头。
func isTopLevelKey(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	switch line[0] {
	case ' ', '\t', '#', '-':
		return false
	}
	return bytes.Contains(line, []byte(":"))
}
