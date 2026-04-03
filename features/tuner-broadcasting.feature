Feature: Tuner Broadcasting
  As a tuner in broadcast mode
  I want to serve channel data and advertise via mDNS
  So that receiving tuners and agents can discover and consume my channels

  Background:
    Given a tuner configured in broadcast mode
    And default preset channels are loaded

  @health
  Scenario: Health endpoint responds
    When a client requests GET /healthz
    Then the response status is 200
    And the response contains "status": "ok"

  @health
  Scenario: Status endpoint shows service info
    When a client requests GET /status
    Then the response contains "service": "tuner"
    And the response contains mode and version

  @channels
  Scenario: List available channels
    When a client requests GET /channels
    Then the response contains preset channels ntp, dns, ping
    And each channel has a name and type

  @channels @preset
  Scenario: NTP preset channel provides live data
    Given the ntp preset channel is enabled
    When the channel source executes
    Then it returns NTP server latency data

  @channels @preset
  Scenario: DNS preset channel provides live data
    Given the dns preset channel is enabled
    When the channel source executes
    Then it returns DNS resolution statistics

  @channels @preset
  Scenario: Ping preset channel provides live data
    Given the ping preset channel is enabled
    When the channel source executes
    Then it returns ICMP latency measurements

  @sse
  Scenario: SSE stream connects and sends heartbeats
    When a client subscribes to GET /channels/ntp/sse
    Then the client receives a connected event
    And the client receives periodic heartbeat events

  @audience
  Scenario: Accept audience metrics
    When a broadcaster posts audience data to /channels/ntp/audience
    Then the metric is stored
    And the status endpoint reflects the audience count

  @caller
  Scenario: Caller line receives messages
    Given caller is enabled in config
    When a viewer posts a message to /channels/ntp/caller
    Then the message is acknowledged
    And the message is available to SSE subscribers

  @llmstxt
  Scenario: Dynamic llms.txt generation
    When a client requests GET /llms.txt
    Then the response is markdown
    And it contains current channel listing
    And it references Gherkin feature files

  @tv
  Scenario: TV channel TOML generation
    When tuner generates TV channel files
    Then TOML files are written to the cable directory
    And each file follows Television channel specification
    And preset channels have watch intervals

  @mdns
  Scenario: mDNS service advertisement
    Given mDNS is enabled
    When tuner starts in broadcast mode
    Then it advertises _tuner._tcp on the local network
    And TXT records include version information

  @signal
  Scenario: Alert signal strength calculation
    Given an alert with severity "critical" triggered 15 minutes ago
    And signal max_age is 30 minutes
    When signal strength is calculated
    Then the strength is approximately 0.5
    And the visualization bar shows half-filled

  @signal
  Scenario: Expired alerts have zero signal
    Given an alert triggered 31 minutes ago
    And signal max_age is 30 minutes
    When signal strength is calculated
    Then the strength is 0.0
