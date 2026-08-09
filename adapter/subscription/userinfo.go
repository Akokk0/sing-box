package subscription

import (
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
)

// userInfoHeader 是机场用来报流量和到期的响应头，形如
//
//	subscription-userinfo: upload=455727941; download=6174315083; total=107374182400; expire=1848124800
//
// 这是 clash 生态的既成约定，不是哪份标准里的东西，所以各家写法参差：大小写、空格、
// 小数、缺字段都见得到。解析要宽容，但不能替机场编数据。
const userInfoHeader = "subscription-userinfo"

// parseUserInfo 解析流量信息。一个字段都没解出来时返回 nil。
//
// 返回 nil 而不是一份全零的结构，是因为大多数机场根本不发这个头。发一份全零出去，
// 面板会画成「已用 0 / 总量 0」——看着就像套餐已经用光了。
func parseUserInfo(header string) *adapter.SubscriptionInfo {
	var info adapter.SubscriptionInfo
	var found bool
	for _, field := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		parsed, err := parseUserInfoValue(strings.TrimSpace(value))
		if err != nil {
			// 单个字段坏掉不该连累其它字段。
			continue
		}
		switch name {
		case "upload":
			info.Upload = parsed
		case "download":
			info.Download = parsed
		case "total":
			info.Total = parsed
		case "expire":
			info.Expire = parsed
		default:
			continue
		}
		found = true
	}
	if !found {
		return nil
	}
	return &info
}

func parseUserInfoValue(value string) (int64, error) {
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed, nil
	}
	// 有的机场写成小数。
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	return int64(parsed), nil
}
