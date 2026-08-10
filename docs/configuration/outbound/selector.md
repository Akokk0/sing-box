### Structure

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

    The selector can only be controlled through the [Clash API](/configuration/experimental#clash-api-fields) currently.

### Fields

#### outbounds

==Required== when `subscriptions` is empty

List of outbound tags to select.

#### subscriptions

!!! question "Since sing-box 1.14.0"

List of [Subscription](/configuration/subscription/) tags supplying this group's members.

Adds to `outbounds` rather than replacing it, so a group can name fixed outbounds alongside an
airport's nodes.

#### filter

!!! question "Since sing-box 1.14.0"

Picks this group's members out of the nodes the subscriptions supplied. See
[Subscription](/configuration/subscription/#filter).

#### default

The default outbound tag. The first outbound will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
