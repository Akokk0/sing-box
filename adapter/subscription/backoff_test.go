package subscription

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 退避表决定了断网之后多久能自愈，边界错一格代价就是路由器多断几个小时。
func TestNextDelayBacksOffAndCapsOut(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour

	for _, testCase := range []struct {
		name     string
		failures int
		nodes    []string
		interval time.Duration
		expected time.Duration
	}{
		{"顺利时就是配置的间隔", 0, []string{"node"}, day, day},
		{"第一次失败等 10 秒", 1, nil, day, 10 * time.Second},
		{"第二次翻倍", 2, nil, day, 20 * time.Second},
		{"第三次再翻倍", 3, nil, day, 40 * time.Second},
		{"第四次", 4, nil, day, 80 * time.Second},
		{"第五次", 5, nil, day, 160 * time.Second},
		// 一个节点都没有 = 整机断网，退避不该超过 5 分钟。
		{"空组时封顶 5 分钟", 6, nil, day, 5 * time.Minute},
		{"空组时连续失败很多次也还是 5 分钟", 40, nil, day, 5 * time.Minute},
		// 还有节点在用，失败只是刷不新，按 interval 封顶就够。
		{"有节点时按 interval 封顶", 40, []string{"node"}, time.Hour, time.Hour},
		// interval 比初始退避还短时，不该反而等得更久。
		{"interval 比初始退避还短", 1, nil, 5 * time.Second, 5 * time.Second},
		{"有节点且 interval 短", 3, []string{"node"}, 15 * time.Second, 15 * time.Second},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			s := &Subscription{interval: testCase.interval, nodes: testCase.nodes}
			require.Equal(t, testCase.expected, s.nextDelay(testCase.failures))
		})
	}
}
