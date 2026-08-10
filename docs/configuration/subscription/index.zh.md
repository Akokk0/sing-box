---
icon: material/new-box
---

!!! question "自 sing-box 1.14.0 起"

# 订阅

订阅让 sing-box 自己去拉机场的节点列表、转成出站、按需分配给策略组，全程不重新生成配置文件、
不重启进程。机场没有改动的节点，它们的出站对象一动不动，正在走它们的连接一条都不会断。

### 结构

```json
{
  "subscriptions": [
    {
      "tag": "airport",
      "url": "https://example.com/api/v1/client/subscribe?token=",
      "format": "mihomo",
      "interval": "24h",
      "download_detour": "",
      "download_timeout": "30s",
      "user_agent": "",
      "path": "/etc/sing-box/airport.yaml",
      "exclude_skipped": false
    }
  ]
}
```

策略组通过 `subscriptions` 字段从订阅取成员，[Selector](../outbound/selector/) 和
[URLTest](../outbound/urltest/) 都支持：

```json
{
  "type": "selector",
  "tag": "🇭🇰 HK",
  "subscriptions": ["airport"],
  "filter": [
    { "action": "include", "keywords": ["🇭🇰|HK|香港"] }
  ]
}
```

### 字段

#### tag

==必填==

订阅的标签。策略组按这个名字引用它，Clash API 也一样——订阅在 `/providers/proxies` 下以
proxy provider 的形式呈现。

#### url

==必填==

订阅地址。

它的路径和查询串等同机场的账号密码，配置文件的权限要照此对待。sing-box 不会把这个地址写进
日志，也不会放进 API 响应。

#### format

订阅内容的格式。目前只支持 `mihomo`（Clash 配置里的 `proxies` 段），也是默认值。

会被转换的协议有 `anytls`、`shadowsocks` 和 `trojan`。其余的按名字跳过并记录，这样机场新加
一个尚未支持的协议不会让整份订阅作废。

#### interval

自动更新间隔，默认 `24h`，最小 `1m`。

内容与上一次逐字节相同的更新不会碰任何出站。

#### download_detour

用哪个出站去拉订阅，默认直连。

留空通常是对的：其它路都不通的时候，更新订阅是唯一的自救手段，而把这个请求绕回一个由该订阅
自己供给的策略组，启动时就是死锁。

#### download_timeout

单次拉取的时限，默认 `30s`。

#### user_agent

拉订阅时发送的 `User-Agent`，留空则用 Go 的默认值。

机场常按这个头决定返回什么格式。默认刻意不改：一个现在能正常工作的机场，很可能正是因为没有
认出我们才给的 Clash YAML。只有在机场明确要求某个客户端名字时才设它。

#### path

本地存档的位置。

启动时先应用这份存档，再在后台去拉新的——路由器开机时 WAN 往往还没通，这样节点照样是齐的。
重启之后面板上「上次更新」的时间就是从这个文件的修改时间恢复的。

#### exclude_skipped

为真时，转换不了的节点连名字都不记。

默认会留下名字：否则机场换协议时节点悄悄少一批，事后完全无从查起。

### 策略组字段

以下字段由 [Selector](../outbound/selector/) 和 [URLTest](../outbound/urltest/) 支持。

#### subscriptions

本组的成员来自这几份订阅。

`outbounds` 和 `subscriptions` 是相加的关系而不是互相替换，所以一个组可以把 `direct` 和机场
的节点写在一起。filter 只作用于订阅送来的那批。

#### filter

从订阅给出的节点里挑出本组的成员，按声明顺序依次应用。`include` 只留下命中的，`exclude`
去掉命中的；命中一条 entry 里的任意关键词即算命中，订阅本身的顺序会被保留。

```json
{
  "filter": [
    { "action": "exclude", "keywords": ["Traffic|Expire|剩余|到期"] },
    { "action": "include", "keywords": ["🇭🇰|🇯🇵|🇸🇬"] }
  ]
}
```

关键词是 Go 的 [RE2](https://github.com/google/re2/wiki/Syntax) 正则，不支持 lookahead。
mihomo 配置里那种 `(?=...)` 写法必须改写成 `include` / `exclude` 两步；写错会当场报错，
而不是悄悄谁也匹配不上。
