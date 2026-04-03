Feature: Tuner Federation Participation
  As a tuner instance
  I want to participate in the smoke-alarm federation as a special-purpose follower
  So that I receive aggregated status from all federated smoke-alarm instances
  # HOW FEDERATION WORKS:
  #   Smoke-alarm uses deterministic slot election via TCP port binding (5100-5103).
  #   The first instance to bind base_port becomes the INTRODUCER (leader).
  #   Subsequent instances become FOLLOWERS. Followers announce to the introducer,
  #   send heartbeats, and receive membership snapshots. The introducer ages out
  #   stale peers and fans out aggregated status.
  #
  # TUNER'S ROLE:
  #   Tuner joins as a special-purpose follower. It claims a slot in a separate
  #   port range (8100-8103) to avoid colliding with smoke-alarm slots (5100-5103).
  #   It announces to the smoke-alarm introducer, receives membership snapshots,
  #   and consumes aggregated status for visualization -- but does NOT run probes
  #   or contribute target health data. It is a read-only federation participant.
  #
  # WHY GO:
  #   The slot election mechanism (TCP port binding + identity persistence),
  #   announcement/heartbeat state machine, and membership protocol are non-trivial.
  #   Reimplementing in Rust would drift from the reference implementation.
  #   Go matches the smoke-alarm protocol exactly.

  Background:
    Given a smoke-alarm introducer is running on port 5100
    And tuner is configured with federation enabled

  @federation @slot
  Scenario: Tuner claims a federation slot
    Given tuner federation port range is 8100-8103
    When tuner starts
    Then it binds a port in the configured range
    And persists its identity to state/federation/identity.json
    And the identity includes role, port, hostname, and instance ID

  @federation @slot
  Scenario: Tuner reclaims previous slot on restart
    Given tuner previously claimed port 8100
    And state/federation/identity.json exists with port 8100
    When tuner restarts
    Then it attempts port 8100 first
    And if successful, reuses the same instance ID

  @federation @announce
  Scenario: Tuner announces to smoke-alarm introducer
    When tuner starts as a federation follower
    Then it POSTs to http://127.0.0.1:5100/introductions
    And the announcement includes tuners identity and role "follower"
    And the introducer returns the current membership snapshot

  @federation @heartbeat
  Scenario: Tuner sends heartbeats after introduction
    Given tuner has successfully announced
    When the heartbeat interval elapses
    Then tuner POSTs to http://127.0.0.1:5100/heartbeats
    And the response includes updated membership
    And tuner applies membership changes to its local registry

  @federation @heartbeat
  Scenario: Tuner survives introducer outage
    Given tuner is sending heartbeats
    When the smoke-alarm introducer goes down
    Then tuner logs a warning
    And continues retrying announcements
    And does not crash or exit

  @federation @membership
  Scenario: Tuner receives full membership snapshot
    Given 3 smoke-alarm instances are federated
    When tuner receives a membership snapshot
    Then it knows about all 3 instances
    And each instance record includes role, port, hostname, last seen time

  @federation @status
  Scenario: Tuner consumes aggregated status
    Given the smoke-alarm introducer fans out status from followers
    When tuner polls the introducers /status endpoint
    Then it receives target health from all federated instances
    And each target is namespaced by source instance

  @federation @viz
  Scenario: Federation membership drives TV channel generation
    Given tuner has membership for 3 smoke-alarm instances
    When tuner generates TV channels
    Then each instance gets its own signal channel
    And a federation overview channel shows all instances
    And the overview includes connection topology

  @federation @readonly
  Scenario: Tuner does not contribute probe data
    Given tuner is a federation follower
    When the introducer queries tuner for status
    Then tuner returns empty target list
    And tuners role metadata indicates "observer"

  @federation @config
  Scenario: Federation disabled by default
    Given a default tuner configuration
    Then federation is disabled
    And no slot election occurs
    And no announcements are sent

  @federation @config
  Scenario: Federation configuration
    Given a tuner config with federation enabled
    Then the config specifies introducer address
    And the config specifies tuner port range
    And heartbeat and announcement intervals are configurable
