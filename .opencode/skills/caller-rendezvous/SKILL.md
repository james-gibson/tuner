---
name: caller-rendezvous
description: Use when a viewer, agent, or lab cluster needs to learn the address a
  tuner observes for it, or needs a message relayed to a channel's subscribers —
  NAT/reflexive-address questions, "what do I look like from over there", "ping
  me back", rendezvous without mDNS, pairing two processes that cannot discover
  each other, or checking that a caller line is reachable at all. Trigger on:
  caller line, caller rendezvous, observed address, reflexive address, STUN,
  TURN, relay my address, ping me back, who am I to the broadcaster, publish a
  callback, subscribe to a channel. Do NOT use for audience metrics (that is the
  reverse direction: /audience) or for mDNS discovery (that is `_tuner._tcp`).
compatibility: opencode
metadata:
  audience: tuner operators and lab agents
  workflow: caller-rendezvous
---

# Caller rendezvous

The caller line (`POST /channels/<name>/caller` + `GET /channels/<name>/sse`) is
the broadcaster's rendezvous channel. It answers two questions that discovery
protocols usually need extra machinery for:

- **What address do you see me coming from?** The acknowledgement carries
  `seen`, the address the tuner observed on the connection — the binding
  response of a STUN request.
- **Can you relay me to someone who is listening?** The message is fanned out
  to every SSE subscriber of that channel, and replayed from a short per-channel
  history to a subscriber that connects next — the relay half of TURN.

It is not a tunnel and it is not a discovery protocol. It is one post and one
subscription, over the broadcast listener the tuner already serves.

**Two different things are called a skill here.** This file is an agent
`SKILL.md` — instructions for a person or agent driving the HTTP surface. The
*runtime* dynamic skill is a different object: a channel-backed capability with
a 42i-certified rung (`tuner/channel/<name>`), specified in
`features/tuner-nested-dynamic-skills.feature`. That spec is explicitly a
next-steps record with no implementation yet, and its adhd surface is absent
(james-gibson/tuner#13). The caller line is the transport such a skill would
use to ask a peer where it was seen; it is not itself the runtime skill.

## Ask what you look like

```sh
curl -sS -X POST http://127.0.0.1:8093/channels/ntp/caller \
  -H 'Content-Type: application/json' \
  -d '{"message":"rendezvous probe","from":"viewer"}'
```

```json
{"status":"received","channel":"ntp","seen":"192.0.2.10:51843"}
```

`seen` is `RemoteAddr` — the socket's own view. A forwarding header is a claim
by an intermediary, not the address a peer would dial, so the tuner does not
use one. If you sit behind a proxy, this answer is your address **to the
proxy**, and that is the honest answer to "what do I look like from here".

Same request as a hurl assertion:

```hurl
# rendezvous.hurl
POST http://127.0.0.1:8093/channels/ntp/caller
Content-Type: application/json
{
  "message": "rendezvous probe",
  "from": "viewer"
}
HTTP 200
[Asserts]
jsonpath "$.status" == "received"
jsonpath "$.seen" matches "^.+:[0-9]+$"
```

## Listen for the other side

```sh
curl -N http://127.0.0.1:8093/channels/ntp/sse
```

```
event: connected
data: {"channel":"ntp"}

event: caller
data: {"channel":"ntp","message":"rendezvous probe","from":"viewer","seen":"192.0.2.10:51843","at":"..."}

event: heartbeat
data: {"time":"..."}
```

Subscribe first and post second when both sides must see each other; the
history (16 messages per channel, in memory) covers a join that races a post,
not an archive. A message posted on one channel never reaches another channel's
subscribers, and a subscriber that cannot keep up loses its message rather than
stalling the line.

## The sequence

1. Both sides subscribe to the same channel.
2. Each posts a message naming the channel it expects the other on.
3. Each reads the other's `seen` from the caller event — or from its own post
   acknowledgement, which is the same value.
4. The initiator dials the address it learned; the observed address is the
   address the *peer* would have to use, not the one the initiator typed.

For a loopback-only lab the address is uninteresting; for a LAN or a NAT it is
the only address that is actually reachable, which is the point.

## Boundaries

- **`caller_enabled: false` refuses with 403.** A broadcaster that has switched
  the line off is not a rendezvous point, and the refusal is the signal.
- **The address is a disclosure, and so is the message.** The line broadcasts
  what you post to every subscriber of that channel. Do not put credentials,
  tokens, or private endpoints in a caller message; post a capability the peer
  can present on a channel you control instead.
- **The line is not authenticated.** Anyone who can reach the broadcast listener
  can post and subscribe. Treat `seen` as a hint and the message as untrusted
  input; the tuner does not vouch for either.
- **It does not certify anything.** A caller message is not evidence of a rung.
  When the runtime dynamic skill exists, its rung comes from the smoke-alarm
  (`smoke-alarm/features/skill-certification.feature`); a `seen` address from
  the caller line is a transport detail, not a certification input.
- **In-memory only.** History and subscribers live in the process; a restart
  loses both.

## Verifying it

```sh
go test ./internal/server/ -run Caller -count=1 -v
```

The tests cover the observed address in the acknowledgement, live relay, replay
to a late subscriber, channel isolation, the 403 when disabled, and the bounded
history.
