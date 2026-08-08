Feature: Tuner Continuous mDNS Discovery
  As a tuner in receive mode
  I want the mDNS browse loop to remain open for the lifetime of the process
  So that smoke-alarm instances coming online after startup are discovered and
  added as live channel sources without restart

  # RELATIONSHIP TO tuner-receiving.feature:
  #   tuner-receiving.feature covers initial discovery at startup.
  #   This file covers the continuous browse requirement: instances that join
  #   the network after tuner has started must still be discovered, and each
  #   newly-discovered smoke-alarm must produce a live channel source.
  #   The browse loop must never close after the first result batch.

  Background:
    Given a tuner configured in receive mode
    And mDNS browsing is enabled
    And the tuner has been running for at least 30 seconds

  # ── post-startup discovery ───────────────────────────────────────────────────

  @discovery @continuous
  Scenario: smoke-alarm announcing after startup is discovered and added as a channel source
    Given no smoke-alarm named "smoke-alarm-late" is known to tuner
    When "smoke-alarm-late" announces "_smoke-alarm._tcp" on the local network
    Then tuner discovers "smoke-alarm-late"
    And a channel source is created for "smoke-alarm-late"
    And no tuner restart is required

  @discovery @continuous
  Scenario: channel is live and receiving data from a post-startup smoke-alarm
    Given "smoke-alarm-late" has been discovered and a channel source created
    When tuner polls the channel source
    Then signal data is available from "smoke-alarm-late"
    And the channel reflects the instance's current health state

  @discovery @continuous
  Scenario: multiple smoke-alarms joining after startup each get their own channel source
    Given 3 smoke-alarm instances announce "_smoke-alarm._tcp" at 5-second intervals after startup
    Then tuner creates a channel source for each instance as it is discovered
    And each channel source is independent

  # ── browse loop lifetime ──────────────────────────────────────────────────────

  @discovery @continuous
  Scenario: browse loop remains open after the initial discovery window closes
    Given 2 smoke-alarms were discovered during the initial browse window
    When the initial browse window closes
    Then the browse loop is still active
    And a third smoke-alarm announcing after the window is discovered

  @discovery @continuous
  Scenario: browse loop is still active after 60 seconds with no new announcements
    Given no smoke-alarm has announced in the past 60 seconds
    When a smoke-alarm announces at 61 seconds
    Then tuner receives the announcement
    And a channel source is created

  # ── TV channel generation ─────────────────────────────────────────────────────

  @discovery @tv
  Scenario: TV channel TOML is generated for a post-startup smoke-alarm
    Given "smoke-alarm-late" has been discovered via mDNS
    When tuner generates TV channels
    Then a TOML file is written for "smoke-alarm-late" in the cable directory
    And the source command points to "smoke-alarm-late"'s endpoint

  @discovery @tv
  Scenario: existing TV channels are not regenerated when a new instance is discovered
    Given tuner already has TV channels for 2 smoke-alarm instances
    When a third smoke-alarm is discovered
    Then a new TOML file is added for the third instance
    And the existing 2 TOML files are unchanged

  # ── instance departure ────────────────────────────────────────────────────────

  @discovery @continuous
  Scenario: channel source is removed when a smoke-alarm deregisters its mDNS record
    Given "smoke-alarm-primary" is a known channel source
    When "smoke-alarm-primary" deregisters its "_smoke-alarm._tcp" mDNS record
    Then the channel source for "smoke-alarm-primary" is removed
    And its TV channel TOML is removed from the cable directory

  @discovery @continuous
  Scenario: remaining channel sources are unaffected when one instance departs
    Given tuner has channel sources for "smoke-alarm-a" and "smoke-alarm-b"
    When "smoke-alarm-a" deregisters
    Then the channel source for "smoke-alarm-b" continues operating
    And signal data from "smoke-alarm-b" is uninterrupted
