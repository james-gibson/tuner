# tuner — Live Data Broadcaster

A live data broadcaster that organises signal sources into named channels and serves them over HTTP and SSE. Other tools on the LAN discover and subscribe to channels without static configuration via mDNS.

---

## Trust after connection

**A caller can connect to an MCP service; whether it is trusted after connecting is
a separate question, and one most transport specifications do not answer.** This lab
measures it. A caller carries a **42i distance** — the extent to which it has
departed from **nominal** (the service level a call promises) against **linear time**
(the shared tick) — and the distance maps to a **rung** that limits what the caller
may read. A caller that keeps departing is removed from the routeable set.

```
 nominal ●────────────  the level a call promises
          ╲
           ╲ distance     +8 / +16 / +32 …
            ●───────────  where it landed (linear time →)
```

One measure, regardless of source — a stale credential, a clock skew, a tampered
receipt, a honeypot contact. Model:
[`PLANE.md`](https://github.com/james-gibson/lab-safety/blob/main/lab/PLANE.md).
Figures:
[`MATH.md`](https://github.com/james-gibson/lab-safety/blob/main/lab/MATH.md).
Full page with references:
[`SPLASH.md`](https://github.com/james-gibson/lab-safety/blob/main/lab/SPLASH.md).

## Channels

Built-in channel types:

| Channel | Signal |
|---------|--------|
| `ntp` | NTP latency and clock offset |
| `dns` | DNS resolution timing and stats |
| `ping` | ICMP round-trip latency |
| custom | User-defined signal sources |

---

## Quick Start

```sh
# Build
go build -o bin/tuner ./cmd/tuner

# Start broadcasting
./bin/tuner --config configs/tuner.yaml

# Subscribe to a channel (pull)
curl http://localhost:8090/channels/ntp

# Subscribe to a channel (push via SSE)
curl -N http://localhost:8090/channels/ntp/stream
```

---

## Modes

**Broadcast mode** — push: channels stream updates to subscribers via SSE.

**Receive mode** — pull: consumers fetch the latest snapshot from the channel endpoint.

---

## Visualisation

tuner supports a companion `tuner-viz` Rust binary for rendered terminal visualisation. When the Rust binary is absent, tuner falls back to ASCII output automatically.

---

## mDNS Discovery

tuner advertises each channel via mDNS (`_tuner._tcp`) so subscribers on the same LAN can find it without configuration:

```sh
# On a subscriber
dns-sd -B _tuner._tcp local.
```

Other lab tools can browse for `_tuner._tcp` and auto-subscribe to channels they care about.

**Discovery is continuous.** tuner opens one browse per service type for the
lifetime of the process and serves before it looks at the network, so a
`_smoke-alarm._tcp` instance that starts ten minutes after tuner is discovered
as it announces — not at the next scan window. A bounded refresh re-resolves
live records so they are not mistaken for departed ones; a record that stops
being refreshed is dropped and reported through `OnRemove`. There is no
pre-advertising scan holding up the broadcast listener.

The expiry is a **view lease, not a trust change**. Nothing derives a 42i rung
from tuner's discovery, and losing a record here must not demote or evict
anything; the window is wide enough (60s floor) that a tuner can roll its
advertisement — withdraw, restart, re-announce, possibly on a new port — without
the view flapping. Trust stays with the cluster that granted it, not with the
continuity of an advertisement.

**A discovered instance is a channel.** Every `_smoke-alarm._tcp` record becomes
a live channel source: `GET /channels` lists it beside the configured presets,
`GET /channels/<instance>` reports its endpoint, and `GET
/channels/<instance>/status` polls that instance's own `/status`. Sources are
keyed by the instance's DNS-SD label, not `host:port`, so a member that rebinds
its health port is updated in place instead of duplicated, and the source is
retired when its record expires. While serving, each discovered instance also
gets its own TOML in the tuner cable directory: a new instance adds one file and
never rewrites the others; a departing one takes its file with it.

See `features/tuner-mdns-continuous.feature`.

---

## Configuration

```yaml
# configs/tuner.yaml
addr: :8090
channels:
  - name: ntp
    type: ntp
    interval: 5s
  - name: dns
    type: dns
    targets:
      - google.com
      - cloudflare.com
    interval: 10s
  - name: ping
    type: ping
    targets:
      - 8.8.8.8
    interval: 2s
```

---

## Directory Structure

```
tuner/
├── cmd/tuner/             — CLI entry point
├── internal/
│   ├── server/            — HTTP + SSE server
│   ├── channelsource/     — live channel-source registry (discovered instances)
│   ├── signal/            — Signal source implementations
│   ├── mdns/              — mDNS advertisement
│   ├── config/            — Config loading
│   ├── caller/            — Outbound HTTP for signal collection
│   └── tv/                — Visualisation layer
├── viz/                   — tuner-viz Rust companion binary
└── features/              — Gherkin acceptance tests
```

---

## In the Lab

tuner is one of several tools that share a common lab environment. See [lab-safety](https://github.com/james-gibson/lab-safety) for a full map of how all tools connect.

Peer tools:
- **lezz.go** — manages tuner as a background service via LaunchAgent
- **ocd-smoke-alarm** — future: tuner channels feed as targets into smoke-alarm health checks
- **adhd** — future: tuner channel data visualised as dashboard lights

---

## See Also

- [lab-safety — full ecosystem overview](https://github.com/james-gibson/lab-safety)
- [ocd-smoke-alarm](https://github.com/james-gibson/smoke-alarm)
- [adhd dashboard](https://github.com/james-gibson/adhd)
