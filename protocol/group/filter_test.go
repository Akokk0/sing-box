package group_test

import (
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"

	"github.com/stretchr/testify/require"
)

// 取自真实订阅：机场把流量和到期信息也伪装成节点排在最前面，它们绝不能进任何策略组。
func subscriptionTags() []string {
	return []string{
		"Traffic Reset：4 Days Left",
		"Expire：2026-12-31",
		"🇭🇰 Hong Kong 01",
		"🇯🇵 Japan 01",
		"🇭🇰 Hong Kong 02",
		"🇺🇸 United States 01",
		"🇦🇺 Australia 01",
	}
}

func TestFilterTags(t *testing.T) {
	t.Run("include keeps only matches, in the original order", func(t *testing.T) {
		selected, err := group.FilterTags(subscriptionTags(), []option.GroupFilter{
			{Action: "include", Keywords: []string{"🇭🇰|HK|香港"}},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"🇭🇰 Hong Kong 01", "🇭🇰 Hong Kong 02"}, selected)
	})

	t.Run("exclude drops matches", func(t *testing.T) {
		selected, err := group.FilterTags(subscriptionTags(), []option.GroupFilter{
			{Action: "exclude", Keywords: []string{"Traffic|Expire|Days Left"}},
		})
		require.NoError(t, err)
		require.NotContains(t, selected, "Traffic Reset：4 Days Left")
		require.NotContains(t, selected, "Expire：2026-12-31")
		require.Len(t, selected, 5)
	})

	// 「美国」这个组曾经因为关键字里带了小写 us 而把 Australia 收了进去——
	// 那台机器在澳大利亚，用户却以为自己在美国。多条 filter 依次应用正是为了修这种事。
	t.Run("filters apply in order", func(t *testing.T) {
		selected, err := group.FilterTags(subscriptionTags(), []option.GroupFilter{
			{Action: "include", Keywords: []string{"US|us|United States|Australia"}},
			{Action: "exclude", Keywords: []string{"🇦🇺|Australia"}},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"🇺🇸 United States 01"}, selected)
	})

	t.Run("no filter keeps everything", func(t *testing.T) {
		selected, err := group.FilterTags(subscriptionTags(), nil)
		require.NoError(t, err)
		require.Len(t, selected, 7)
	})

	// RE2 不支持 lookahead。悄悄谁也匹配不上的话，组会莫名其妙变空，而用户完全不知道
	// 是自己的正则写错了。
	t.Run("a regex RE2 cannot compile is an error, not an empty group", func(t *testing.T) {
		_, err := group.FilterTags(subscriptionTags(), []option.GroupFilter{
			{Action: "include", Keywords: []string{"^(?=.*HK).*$"}},
		})
		require.Error(t, err)
	})

	t.Run("an unknown action is an error", func(t *testing.T) {
		_, err := group.FilterTags(subscriptionTags(), []option.GroupFilter{
			{Action: "keep", Keywords: []string{"HK"}},
		})
		require.Error(t, err)
	})
}
