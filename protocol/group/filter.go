package group

import (
	"regexp"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// FilterTags 按 filter 从 tags 里挑出成员，保持 tags 的原始顺序。
//
// 顺序要保留：机场给的节点顺序通常是有意义的（同一地区里编号靠前的往往是主力线路），
// 而 urltest 在没有测速历史时就是取第一个。
func FilterTags(tags []string, filters []option.GroupFilter) ([]string, error) {
	if len(filters) == 0 {
		return tags, nil
	}
	compiled := make([][]*regexp.Regexp, len(filters))
	for i, filter := range filters {
		switch filter.Action {
		case "include", "exclude":
		default:
			return nil, E.New("filter[", i, "]: unknown action: ", filter.Action)
		}
		for _, keyword := range filter.Keywords {
			// RE2 不支持 lookahead。与其让一条写错的规则悄悄谁也匹配不上，不如当场报错。
			pattern, err := regexp.Compile(keyword)
			if err != nil {
				return nil, E.Cause(err, "filter[", i, "]: keyword ", keyword)
			}
			compiled[i] = append(compiled[i], pattern)
		}
	}
	selected := make([]string, 0, len(tags))
	for _, tag := range tags {
		keep := true
		for i, filter := range filters {
			matched := matchAny(compiled[i], tag)
			if (filter.Action == "include" && !matched) || (filter.Action == "exclude" && matched) {
				keep = false
				break
			}
		}
		if keep {
			selected = append(selected, tag)
		}
	}
	return selected, nil
}

func matchAny(patterns []*regexp.Regexp, tag string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(tag) {
			return true
		}
	}
	return false
}
