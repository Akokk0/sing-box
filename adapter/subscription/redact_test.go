package subscription

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactErrorDropsThePathAndQuery(t *testing.T) {
	t.Parallel()

	err := redactError(&url.Error{
		Op:  "Get",
		URL: "https://sub.example.com/api/v1/client/subscribe?token=SECRET",
		Err: errors.New("connection refused"),
	})
	require.EqualError(t, err, "Get https://sub.example.com: connection refused")
}

// 有的机场把凭据放在 userinfo 里。url.URL.Host 不含 userinfo，这里把它钉住。
func TestRedactErrorDropsUserInfo(t *testing.T) {
	t.Parallel()

	err := redactError(&url.Error{
		Op:  "Get",
		URL: "https://user:SECRET@sub.example.com/sub",
		Err: errors.New("connection refused"),
	})
	require.NotContains(t, err.Error(), "SECRET")
	require.Contains(t, err.Error(), "sub.example.com")
}

// URL 解析不了时不能把原文回显出去——那正是我们要藏的东西。
func TestRedactErrorSaysNothingWhenTheURLCannotBeParsed(t *testing.T) {
	t.Parallel()

	err := redactError(&url.Error{
		Op:  "Get",
		URL: "://SECRET",
		Err: errors.New("connection refused"),
	})
	require.NotContains(t, err.Error(), "SECRET")
}

func TestRedactErrorLeavesOtherErrorsAlone(t *testing.T) {
	t.Parallel()

	cause := errors.New("subscription is too large")
	require.Equal(t, cause, redactError(cause))
}

// 错误常常是被包过的，遮蔽必须顺着链找。
func TestRedactErrorUnwrapsToFindTheURLError(t *testing.T) {
	t.Parallel()

	wrapped := errors.Join(errors.New("read subscription"), &url.Error{
		Op:  "Get",
		URL: "https://sub.example.com/sub?token=SECRET",
		Err: errors.New("connection refused"),
	})
	require.NotContains(t, redactError(wrapped).Error(), "SECRET")
}
