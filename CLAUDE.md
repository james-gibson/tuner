# CLAUDE.md

## What This Is

`tuner` is a channel broadcaster that bridges smoke-alarm's distributed health data into Television's channel-based visualization paradigm. It operates in two modes:

- **broadcast**: listens on a local port, advertises via mDNS, exposes live channels over HTTP and SSE
- **receive**: discovers broadcast instances via mDNS and subscribes to their signal feeds

Sibling binaries: **adhd**, **ocd-smoke-alarm**, **lezz** — all Go modules under `../`. They can interoperate during tests via subprocess spawning.

## Build & Test

```sh
go build ./cmd/tuner
go test ./...
go vet ./...
```

## Key CLI Commands

```sh
tuner serve --mode=broadcast
tuner serve --mode=receive
tuner channel <name> [--launch]
tuner tv list|launch|sync
tuner discover
tuner signals [--endpoint=<url>]
tuner mdns [--duration=8s]
tuner validate [--config=<path>]
tuner version
```

Config is optional — built-in defaults are used when `--config` is omitted.

## Architecture

```
cmd/tuner/        CLI entrypoint, flag parsing, subcommand dispatch
internal/
  config/         YAML config loading, Defaults(), schema structs
  server/         HTTP handlers: /channels, /channels/<name>, /llms.txt, /healthz
  mdns/           mDNS advertiser and browser (grandcat/zeroconf)
  tv/             Television TOML channel generation, preset library
  signal/         Alert signal strength calculation and bar visualization
  llms/           llms.txt generator for LLM agent discovery
viz/              Rust TUI visualization binary (tuner-viz)
features/         Gherkin requirements
configs/          Sample config (optional — binary runs without it)
```

## Key Constraint

tuner must not require a config file to start. All subcommands default to built-in values when `--config` is omitted. Config files are for overrides only.
