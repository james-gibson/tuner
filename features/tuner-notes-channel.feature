# The notes channel: an RSS-shaped feed of prep questions, surfaced on the live
# broadcast. Content is authored elsewhere (kb/scripts/lab/search/notes.py); tuner
# only presents the newest items. See internal/notes/notes.go.

@channels @notes
Feature: A notes channel surfaces the latest prep questions
  As someone watching the broadcast
  I want the newest questions and gaps on a channel
  So that prep is visible without asking any one agent

  Scenario: a notes channel is listed beside the presets
    Given a config with a notes channel "prep" pointing at a feed
    When I request the channel list
    Then the list contains "prep" with type "notes"

  Scenario: the channel serves the feed's items
    Given a notes feed with two items
    When I request the channel "prep"
    Then the response contains 2 items
    And each item carries its kind

  Scenario: a missing feed is reported, not hidden
    Given a notes channel whose source does not exist
    When I request the channel "prep"
    Then the response carries an error
    And the channel is still listed

  Scenario: a feed is cached for the refresh window
    Given a notes feed fetched once
    When the channel is requested twice inside the window
    Then the source is fetched once
