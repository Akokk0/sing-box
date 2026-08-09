package subscription

import (
	"testing"
)

func TestParseUserInfo(t *testing.T) {
	// 机场返回的原样格式。
	t.Run("a real header", func(t *testing.T) {
		info := parseUserInfo("upload=455727941; download=6174315083; total=107374182400; expire=1848124800")
		if info == nil {
			t.Fatal("parseUserInfo returned nil for a valid header")
		}
		if info.Upload != 455727941 {
			t.Errorf("Upload = %d, want 455727941", info.Upload)
		}
		if info.Download != 6174315083 {
			t.Errorf("Download = %d, want 6174315083", info.Download)
		}
		if info.Total != 107374182400 {
			t.Errorf("Total = %d, want 107374182400", info.Total)
		}
		if info.Expire != 1848124800 {
			t.Errorf("Expire = %d, want 1848124800", info.Expire)
		}
	})

	// 各家机场的写法并不统一：大小写、空格都可能不一样。
	t.Run("tolerates case and spacing", func(t *testing.T) {
		info := parseUserInfo("Upload=1 ;  DOWNLOAD = 2 ; Total=3")
		if info == nil {
			t.Fatal("parseUserInfo returned nil")
		}
		if info.Upload != 1 || info.Download != 2 || info.Total != 3 {
			t.Errorf("got %+v, want upload=1 download=2 total=3", info)
		}
	})

	// 不限期的套餐通常就不带 expire。缺字段留零，不能因此把整条丢掉。
	t.Run("keeps what is present when a field is missing", func(t *testing.T) {
		info := parseUserInfo("upload=1; download=2; total=3")
		if info == nil {
			t.Fatal("parseUserInfo returned nil for a header without expire")
		}
		if info.Expire != 0 {
			t.Errorf("Expire = %d, want 0", info.Expire)
		}
		if info.Total != 3 {
			t.Errorf("Total = %d, want 3", info.Total)
		}
	})

	// 有的机场把数值写成小数。
	t.Run("accepts a float value", func(t *testing.T) {
		info := parseUserInfo("total=1073741824.0")
		if info == nil || info.Total != 1073741824 {
			t.Errorf("got %+v, want total=1073741824", info)
		}
	})

	// 大多数机场根本不返回这个头。没有就是没有，不能编一份全零的出来——
	// 面板会把它画成「已用 0 / 总量 0」，看着像套餐用光了。
	t.Run("returns nil when there is nothing to report", func(t *testing.T) {
		for _, header := range []string{"", "   ", "nonsense", "upload=; download="} {
			if info := parseUserInfo(header); info != nil {
				t.Errorf("parseUserInfo(%q) = %+v, want nil", header, info)
			}
		}
	})

	// 单个字段坏掉不该连累其它字段。
	t.Run("skips an unparsable field", func(t *testing.T) {
		info := parseUserInfo("upload=abc; download=2")
		if info == nil {
			t.Fatal("one bad field threw the whole header away")
		}
		if info.Upload != 0 || info.Download != 2 {
			t.Errorf("got %+v, want upload=0 download=2", info)
		}
	})
}
