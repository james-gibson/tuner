Feature: Tuner Custom Channels
  As a tuner in broadcast mode
  I want a channels.custom entry to serve its own source
  So that a channel that appears on /channels and in llms.txt answers with data

  @custom @command
  Scenario: A command channel serves its output
    Given a custom channel with source type command
    When a client requests GET /channels/<name>
    Then the command is executed
    And the response contains the command's output as data
    And the output is parsed as JSON when it is JSON

  @custom @http
  Scenario: An http channel is polled for its refresh window
    Given a custom channel with source type http
    When a client requests GET /channels/<name> twice inside refresh
    Then the source is fetched once
    And the second client is served the cached snapshot
    And a non-2xx response from the source is reported as bad gateway

  @custom @sse
  Scenario: An sse channel keeps the first event of its stream
    Given a custom channel with source type sse
    When a client requests GET /channels/<name>
    Then tuner opens the event stream
    And keeps the first event with data
    And skips comment keep-alives

  @custom @webhook
  Scenario: A webhook channel waits for a push
    Given a custom channel with source type webhook
    When a client requests GET /channels/<name> before anything is pushed
    Then the response says the channel is waiting
    And no source is fetched

  @custom @webhook @ingest
  Scenario: A webhook channel serves what was pushed to it
    Given a custom channel with source type webhook
    When a client posts a value to POST /channels/<name>/ingest
    Then the value is stored as the channel's snapshot
    And GET /channels/<name> serves it
    And GET /channels/<name>/sse emits it as a snapshot event

  @custom @ingest @guard
  Scenario: A remote push is refused without a token
    Given a webhook channel with no auth token
    When a peer that is not this machine posts to POST /channels/<name>/ingest
    Then the response status is 403
    And nothing is stored
    And the refusal happens before the body is read

  @custom @ingest @guard
  Scenario: A token admits a remote push
    Given a webhook channel with an auth token
    When a peer posts to POST /channels/<name>/ingest without the token
    Then the response status is 401
    When the same peer posts with the token
    Then the response status is 200

  @custom @ingest
  Scenario: Only a webhook channel accepts ingest
    Given a custom channel with source type http
    When a client posts to POST /channels/<name>/ingest
    Then the response status is 405

  @custom @config
  Scenario: A misconfigured channel says what is wrong
    Given a custom channel whose source type is unknown
    When a client requests GET /channels
    Then the channel is listed with status misconfigured
    And the listing names the unknown source type
    When a client requests GET /channels/<name>
    Then the response status is 502
    And the response names the unknown source type
    When a client requests GET /llms.txt
    Then the channel is advertised as misconfigured
    When tuner validate runs
    Then it exits non-zero and lists the problem

  @custom @config
  Scenario: A reachable listener is warned about a tokenless ingest
    Given a webhook channel with no auth token
    And a broadcast listener that is not loopback
    When tuner starts
    Then it logs that ingest on that channel is loopback-only
    And names auth.token as the way to accept a remote push

  @custom @broadcast
  Scenario: Owning the data routes keeps the broadcast actions
    Given a custom channel
    When a client posts audience data to /channels/<name>/audience
    Then the metric is stored
