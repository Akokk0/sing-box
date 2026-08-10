### Structure

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
  "tolerance": 0,
  "idle_timeout": "",
  "interrupt_exist_connections": false
}
```

### Fields

#### outbounds

==Required== when `subscriptions` is empty

List of outbound tags to test.

#### subscriptions

!!! question "Since sing-box 1.14.0"

List of [Subscription](/configuration/subscription/) tags supplying this group's members.

Adds to `outbounds` rather than replacing it. Members added by an update are tested straight
away, so a new node does not have to wait out an interval before it can be selected.

#### filter

!!! question "Since sing-box 1.14.0"

Picks this group's members out of the nodes the subscriptions supplied. See
[Subscription](/configuration/subscription/#filter).

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### tolerance

The test tolerance in milliseconds. `50` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
