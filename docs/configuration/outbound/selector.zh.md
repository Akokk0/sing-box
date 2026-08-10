### 结构

```json
{
  "type": "selector",
  "tag": "select",

  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "subscriptions": [],
  "filter": [],
  "default": "proxy-c",
  "interrupt_exist_connections": false
}
```

!!! quote ""

    选择器目前只能通过 [Clash API](/zh/configuration/experimental/clash-api/) 来控制。

### 字段

#### outbounds

`subscriptions` 为空时 ==必填==

用于选择的出站标签列表。

#### subscriptions

!!! question "自 sing-box 1.14.0 起"

供给本组成员的[订阅](/zh/configuration/subscription/)标签列表。

与 `outbounds` 是相加而不是替换的关系，所以一个组可以把固定的出站和机场的节点写在一起。

#### filter

!!! question "自 sing-box 1.14.0 起"

从订阅送来的节点里挑出本组的成员。见[订阅](/zh/configuration/subscription/#filter)。

#### default

默认的出站标签。默认使用第一个出站。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。