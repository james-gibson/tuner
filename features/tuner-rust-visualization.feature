Feature: Rust Visualization Integration
  As a tuner operator
  I want rich terminal animations for signal strength and alerts
  So that monitoring is visually clear and responsive

  # WHY GO FOR THE APP SHELL:
  #   Smoke-alarm federation uses a distributed coordination protocol:
  #   deterministic slot election (TCP port binding on 5100-5103), introducer/
  #   follower peer negotiation, heartbeat-driven membership, and status fan-out.
  #   A Go tuner can participate as a special-purpose federation follower --
  #   claiming a slot, announcing to the introducer, receiving membership
  #   snapshots, and consuming aggregated status. Reimplementing slot election,
  #   identity persistence, and the announcement/heartbeat state machine in Rust
  #   would be non-trivial and drift from the reference implementation.
  #
  # WHY RUST FOR VISUALIZATION:
  #   Television (the fuzzy finder we use for display) is Rust-based and uses
  #   ratatui for TUI rendering. To produce rich animations (signal decay bars,
  #   alert pulse effects, federation topology maps) that render correctly inside
  #   Television's preview pane, the visualization code must output ratatui-
  #   compatible terminal sequences. A small Rust CLI tool ("tuner-viz") does
  #   this natively, and can use tachyonfx for animation effects.
  #
  #   Go's terminal libraries (bubbletea, lipgloss) produce ANSI output that
  #   works standalone but can conflict with Television's own ratatui rendering
  #   loop when invoked as a subprocess in the [source] command field.
  #
  # ARCHITECTURE:
  #   tuner (Go) -- writes TOML --> Television (Rust/ratatui) -- invokes --> tuner-viz (Rust)
  #   tuner (Go) -- slot election + heartbeats --> smoke-alarm federation (Go)
  #   tuner (Go) -- HTTP/JSON status --> smoke-alarm health endpoints (Go)
  #
  # BOUNDARY:
  #   tuner-viz is a standalone CLI binary. It reads JSON from stdin or HTTP,
  #   outputs styled terminal text. Television calls it via the TOML [source]
  #   command field. No FFI, no shared memory, no mixed builds. Go project
  #   builds and runs without Rust toolchain (ASCII fallback).

  Background:
    Given tuner-viz is installed and on PATH
    And tuner is configured with TV channel presets

  @viz @signal
  Scenario: Signal strength bar rendering
    Given a JSON alert payload with severity "critical" and age 15 minutes
    When tuner-viz renders signal strength
    Then it outputs a colored bar with 50% fill
    And the bar uses ratatui-compatible ANSI sequences
    And the output is valid for Television preview pane

  @viz @signal
  Scenario: Multiple signal bars for discovered services
    Given JSON payloads for 3 smoke-alarm services
    When tuner-viz renders a signal dashboard
    Then each service shows a labeled bar
    And bars are sorted by signal strength descending
    And expired signals (age > max_age) show empty bars

  @viz @alert
  Scenario: Alert pulse animation via tachyonfx
    Given an active critical alert
    When tuner-viz renders with animation enabled
    Then the alert indicator pulses using tachyonfx effects
    And the animation frame rate respects Television's watch interval

  @viz @federation
  Scenario: Federation topology map
    Given federation membership data as JSON
    When tuner-viz renders topology
    Then it displays nodes with connection lines
    And healthy nodes are green, degraded amber, outage red
    And the current instance is highlighted

  @viz @fallback
  Scenario: Graceful fallback without tuner-viz
    Given tuner-viz is NOT installed
    When tuner generates TV channel TOML files
    Then the source command falls back to plain text output
    And signal bars use ASCII characters (no ratatui dependency)
    And all channels remain functional

  @viz @fallback
  Scenario: Go ASCII fallback matches signal calculation
    Given a Go-rendered ASCII signal bar for severity "critical" age 15m
    And a tuner-viz rendered signal bar for the same input
    Then both show approximately 50% fill
    And the signal strength value is identical

  @integration @tv
  Scenario: Television channel uses tuner-viz command
    Given tuner generates a TV channel with tuner-viz available
    Then the TOML source command invokes tuner-viz
    And the preview command invokes tuner-viz with --preview flag
    And the watch interval matches the configured refresh rate

  @integration @tv
  Scenario: Television channel uses Go fallback command
    Given tuner generates a TV channel without tuner-viz available
    Then the TOML source command uses curl/jq/shell pipeline
    And the preview command uses simple text rendering
    And the channel is fully functional without Rust

  @build
  Scenario: Rust crate builds independently
    Given the tuner-viz Cargo.toml in tuner/viz/
    When cargo build --release is run
    Then the binary compiles without tuner Go code
    And the binary has no runtime dependency on tuner
    And the binary size is under 5MB

  @build
  Scenario: Go project builds without Rust
    Given the tuner Go module
    When go build is run without Rust toolchain
    Then the build succeeds
    And all Go tests pass
    And ASCII fallback is used for all visualizations
