package subscription

import (
	"io"

	E "github.com/sagernet/sing/common/exceptions"
)

// maxSubscriptionSize 是一份订阅原文的上限。
//
// 真实的机场订阅是几十到几百 KB，这个上限宽出好几个数量级，正常订阅碰不到它。
// 它防的是另一件事：一个故障或恶意的服务器可以无限地送数据，而这东西跑在路由器上，
// 内存被吃光的后果是 sing-box 被 OOM 杀掉——也就是整个网断掉。
const maxSubscriptionSize = 16 << 20 // 16 MiB

// readAtMost 读取至多 limit 字节，超出则报错。
//
// 刻意不截断。截出来的 YAML 多半仍然合法，只是尾巴上少了一批节点，而那会被当成
// 「机场撤掉了这些节点」——于是一大批出站被摘掉、策略组瞬间缩水。报错则什么都不动，
// 上一次的好状态原样保住。
func readAtMost(reader io.Reader, limit int64) ([]byte, error) {
	// 多读一个字节：读满 limit 说明不了什么，读到 limit+1 才能断定超限。
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, E.New("subscription is too large: over ", limit, " bytes")
	}
	return content, nil
}
