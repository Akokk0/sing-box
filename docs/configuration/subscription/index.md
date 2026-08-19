---
icon: material/new-box
---

!!! question "Since sing-box 1.14.0"

# Subscription

A subscription lets sing-box fetch an airport's node list itself, convert it into outbounds and
hand those to policy groups, without regenerating the configuration file or restarting the
process. Nodes the airport did not touch keep their outbound object, so connections running
through them are not interrupted.

### Structure

```json
{
  "subscriptions": [
    {
      "tag": "airport",
      "url": "https://example.com/api/v1/client/subscribe?token=",
      "format": "mihomo",
      "interval": "24h",
      "http_client": {},
      "download_timeout": "30s",
      "user_agent": "",
      "path": "/etc/sing-box/airport.yaml",
      "exclude_skipped": false
    }
  ]
}
```

A group takes its members from a subscription with the `subscriptions` field, which
[Selector](../outbound/selector/) and [URLTest](../outbound/urltest/) both accept:

```json
{
  "type": "selector",
  "tag": "🇭🇰 HK",
  "subscriptions": ["airport"],
  "filter": [
    { "action": "include", "keywords": ["🇭🇰|HK|Hong Kong"] }
  ]
}
```

### Fields

#### tag

==Required==

The tag of the subscription. Groups refer to it by this name, and so does the Clash API, which
presents subscriptions as proxy providers under `/providers/proxies`.

#### url

==Required==

The subscription address.

Its path and query are the airport's credentials. Treat the configuration file's permissions
accordingly; sing-box keeps the address out of its logs and out of API responses.

#### format

The format of the subscription content. Only `mihomo` — the `proxies` section of a Clash
configuration — is supported, and it is the default.

`anytls`, `shadowsocks` and `trojan` nodes are converted. Anything else is skipped by name, so
a protocol the airport newly introduced does not invalidate the rest of the list.

#### interval

How often to fetch, `24h` by default. The minimum is `1m`.

An update whose content is byte-identical to the last one touches no outbound at all.

#### http_client

The HTTP client used to fetch. Set the outbound (`detour`), the resolver (`domain_resolver`)
and TLS here, or name an existing client by its tag. See [HTTP Client](/configuration/shared/http-client/).

Leaving `detour` empty is usually right: updating the subscription is the only way out when
nothing else works, and routing that request back through a group the subscription itself
supplies deadlocks at startup.

`domain_resolver` needs the same care, and it is the easier one to get wrong. With it unset
the subscription's hostname is resolved through `route.default_domain_resolver` — commonly a
DNS server that itself only works through the proxy. Delete the local archive and the loop
closes: the name cannot be resolved, so the nodes never arrive, so the name still cannot be
resolved. Point it at a resolver that does not depend on the proxy:

```json
{
  "url": "https://example.org/subscribe?token=...",
  "http_client": {
    "domain_resolver": "local-resolver"
  }
}
```

#### download_timeout

The time limit for one fetch, `30s` by default.

#### user_agent

The `User-Agent` to send. Go's default is used if empty.

Airports often pick a format from this header. The default is deliberately left alone: an
airport that works today is quite possibly returning Clash YAML precisely because it did not
recognise the client. Set it only when an airport demands a name it knows.

#### path

Where to keep a copy of the last fetched subscription.

At startup the copy is applied first and the network fetch follows in the background, so a
router that boots before its WAN is up still has its nodes. Its modification time is what the
dashboard reports as the last update after a restart.

#### exclude_skipped

Drop nodes that could not be converted instead of logging their names.

Names are kept by default: an airport switching protocols otherwise loses a batch of nodes
with nothing to show for it.

### Group fields

These are accepted by [Selector](../outbound/selector/) and [URLTest](../outbound/urltest/).

#### subscriptions

The subscriptions this group draws its members from.

`outbounds` and `subscriptions` add up rather than replace each other, so a group can name
`direct` alongside an airport's nodes. Filters apply only to what the subscription supplied.

#### filter

Picks this group's members out of the subscription's nodes, applied in the order written.
`include` keeps what matches, `exclude` drops it; a node matching any keyword in an entry
counts as a match, and the subscription's own order is preserved.

```json
{
  "filter": [
    { "action": "exclude", "keywords": ["Traffic|Expire|Days Left"] },
    { "action": "include", "keywords": ["🇭🇰|🇯🇵|🇸🇬"] }
  ]
}
```

Keywords are Go [RE2](https://github.com/google/re2/wiki/Syntax) regular expressions, which
have no lookahead. A mihomo filter written as `(?=...)` has to be rewritten as an `include`
and an `exclude` step; it is reported as an error rather than silently matching nothing.
