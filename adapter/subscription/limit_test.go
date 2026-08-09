package subscription

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReadAtMost(t *testing.T) {
	t.Run("passes content through untouched", func(t *testing.T) {
		content, err := readAtMost(strings.NewReader("proxies: []"), 1024)
		if err != nil {
			t.Fatalf("readAtMost: %v", err)
		}
		if string(content) != "proxies: []" {
			t.Errorf("got %q, want %q", content, "proxies: []")
		}
	})

	t.Run("accepts content of exactly the limit", func(t *testing.T) {
		// 边界必须收下，不然一个刚好压线的订阅会被无缘无故拒掉。
		content, err := readAtMost(bytes.NewReader(bytes.Repeat([]byte("a"), 64)), 64)
		if err != nil {
			t.Fatalf("readAtMost at exactly the limit: %v", err)
		}
		if len(content) != 64 {
			t.Errorf("got %d bytes, want 64", len(content))
		}
	})

	// 超限必须报错，绝不能截断。截断出来的 YAML 往往仍然合法，只是少了后面一批节点——
	// 那会被当成「机场撤掉了这些节点」，于是一大批出站被摘掉，策略组瞬间缩水。
	// 报错则什么都不动，保住上一次的好状态。
	t.Run("refuses oversized content instead of truncating", func(t *testing.T) {
		_, err := readAtMost(bytes.NewReader(bytes.Repeat([]byte("a"), 65)), 64)
		if err == nil {
			t.Fatal("readAtMost accepted content past the limit")
		}
		if !strings.Contains(err.Error(), "too large") {
			t.Errorf("error %q does not say the content was too large", err)
		}
	})

	// 一个恶意或故障的服务器可以无限地送数据。读取必须在上限处停下，
	// 而不是一直读到把路由器的内存吃光。
	t.Run("stops reading an endless stream", func(t *testing.T) {
		endless := &countingReader{}
		_, err := readAtMost(endless, 1024)
		if err == nil {
			t.Fatal("readAtMost accepted an endless stream")
		}
		if endless.read > 64*1024 {
			t.Errorf("read %d bytes before giving up on a 1024-byte limit", endless.read)
		}
	})
}

// countingReader 永远有数据可读，并记下总共被读走了多少。
type countingReader struct{ read int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.read += len(p)
	return len(p), nil
}

var _ io.Reader = (*countingReader)(nil)
