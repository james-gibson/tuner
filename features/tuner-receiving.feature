Feature: Tuner Receiving
  As a tuner in receive mode
  I want to discover broadcasting tuners and smoke-alarm services
  So that I can observe and display their channels via Television

  Background:
    Given a tuner configured in receive mode
    And mDNS browsing is enabled

  @discovery
  Scenario: Discover smoke-alarm services
    Given a smoke-alarm is advertising _smoke-alarm._tcp on the network
    When tuner browses for services
    Then it discovers the smoke-alarm instance
    And extracts TXT records for version and endpoints

  @discovery
  Scenario: Discover broadcasting tuners
    Given a tuner is advertising _tuner._tcp on the network
    When tuner browses for services
    Then it discovers the broadcasting tuner
    And can list its available channels

  @subscribe
  Scenario: Subscribe to a public channel
    Given a broadcasting tuner with channel "ntp"
    When the receiving tuner subscribes to the channel
    Then it receives SSE events with channel data

  @subscribe
  Scenario: Subscribe to a private channel with join code
    Given a broadcasting tuner with a private warroom channel
    And a valid join code
    When the receiving tuner subscribes with the join code
    Then it receives SSE events with channel data

  @subscribe
  Scenario: Reject subscription with invalid join code
    Given a broadcasting tuner with a private warroom channel
    And an invalid join code
    When the receiving tuner attempts to subscribe
    Then the subscription is rejected

  @tv
  Scenario: Generate TV channel from discovered service
    Given a smoke-alarm service at 192.168.1.50:18088
    When tuner generates a TV channel for it
    Then a TOML file is created in the cable directory
    And the source command points to the service endpoint

  @tv
  Scenario: Open Television with generated channel
    Given TV auto_launch is enabled
    And a channel TOML has been generated
    When the user requests to view the channel
    Then Television is launched with the channel loaded

  @signal
  Scenario: Display signal strength from multiple sources
    Given three discovered smoke-alarm services
    And each has alerts with varying severity and age
    When signal strength is rendered
    Then each service shows a signal bar
    And critical recent alerts show strongest signal

  @persistence
  Scenario: Persist subscriptions across restarts
    Given the receiving tuner has active subscriptions
    When the tuner shuts down and restarts
    Then previous subscriptions are restored
    And channels reconnect automatically

  @caller
  Scenario: Send caller message to broadcasting tuner
    Given a broadcasting tuner with caller enabled
    When the receiving tuner sends a caller message
    Then the message reaches the broadcaster
    And smoke-alarm can observe the message via MCP tool response
