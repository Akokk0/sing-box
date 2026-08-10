### 结构

```json
{
  "type": "urltest",
  "tag": "auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "subscriptions": [],
  "filter": [],
  "url": "",
  "interval": "",
  "tolerance": 50,
  "idle_timeout": "",
  "interrupt_exist_connections": false
}
```

### 字段

#### outbounds

`subscriptions` 为空时 ==必填==

用于测试的出站标签列表。

#### subscriptions

!!! question "自 sing-box 1.14.0 起"

供给本组成员的[订阅](/zh/configuration/subscription/)标签列表。

与 `outbounds` 是相加而不是替换的关系。更新带来的新成员会立刻被测一次，不必等满一个
`interval` 才有资格被选中。

#### filter

!!! question "自 sing-box 1.14.0 起"

从订阅送来的节点里挑出本组的成员。见[订阅](/zh/configuration/subscription/#filter)。

#### url

用于测试的链接。默认使用 `https://www.gstatic.com/generate_204`。

#### interval

测试间隔。 默认使用 `3m`。

#### tolerance

以毫秒为单位的测试容差。 默认使用 `50`。

#### idle_timeout

空闲超时。默认使用 `30m`。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。