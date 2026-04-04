# tuner — Live Data Broadcaster

A live data broadcaster that organises signal sources into named channels and serves them over HTTP and SSE. Other tools on the LAN discover and subscribe to channels without static configuration via mDNS.

---

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
