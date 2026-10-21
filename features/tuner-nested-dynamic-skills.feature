# Next-steps record — spec-only, no implementation yet (tuner has no skill
# creation, composition or publication code; skills appear today only as a
# panel that features/tuner-feature-browser.feature assumes already exists).
#
# These are NOT the `SKILL.md` agent skills. A skill here is a runtime
# capability with a 42i-certified rung, as defined by
# smoke-alarm/features/skill-certification.feature: existence (rung 1),
# equality (rung 2), isotope certification (rung 3+), with a distance equal to
# the number of proxy hops between the skill and the certifying alarm.
#
# Division of labour:
#   smoke-alarm  certifies rungs — never tuner, never the skill's host
#   adhd         exposes the certified surface (adhd.skills.certified)
#   tuner        creates the skills, nests them, and publishes the inventory
#
# The adhd side of that surface is declared, absent (james-gibson/tuner#13).

@optional
Feature: Tuner Nested Dynamic Skills — Creation, Composition and Publication
  As a tuner operator
  I want tuner to create skills from its live channels and compose them into
  nested skills
  So that composed capabilities are addressable, routable, and certified by the
  smoke-alarm like any other capability

  Background:
    Given tuner is broadcasting on the local network
    And the cluster's smoke-alarm is certifying skill rungs

  # ── creation ────────────────────────────────────────────────────────────────

  Scenario: tuner creates a dynamic skill for each published channel
    Given tuner is broadcasting channel "ntp"
    When tuner starts the channel
    Then it exposes a skill named "tuner/channel/ntp"
    And the skill's declared level is the channel's nominal
    And the skill reports rung 0 until the smoke-alarm certifies it

  Scenario: a skill's identity is deterministic across restarts
    Given channel "ntp" was published with skill "tuner/channel/ntp"
    When tuner restarts and republishes the same channel
    Then the skill identity is unchanged
    And no second skill is registered for the same channel

  Scenario: tuner never self-certifies
    Given tuner has just created a skill
    Then the skill's certified_rung is 0
    And only the smoke-alarm can raise it
    And tuner does not accept a rung asserted by the skill itself

  # ── nesting ─────────────────────────────────────────────────────────────────

  Scenario: a nested skill composes its children
    Given skills "tuner/channel/ntp" and "tuner/channel/dns" exist
    When the operator composes them into "tuner/network-health"
    Then the nested skill lists both children
    And invoking it invokes each child

  Scenario: nesting depth becomes hop distance
    Given "tuner/channel/ntp" is hosted by tuner itself at distance 0
    And "tuner/network-health" nests it one level down
    Then the nested skill's distance is 1
    And each further nesting level adds one hop, priced at +8 on the 42i ladder

  Scenario: a nested skill's rung is bounded by its weakest child
    Given child "a" is certified at rung 3
    And child "b" is certified at rung 1
    When "tuner/network-health" nests both
    Then the nested skill is reported at rung 1
    And the reason names child "b"

  Scenario: a cycle is refused, not resolved
    Given skill "a" nests "b"
    And skill "b" nests "a"
    When tuner evaluates the composition graph
    Then it refuses the composition and names the cycle
    # Nesting is a DAG. A cycle makes hop distance non-terminating and would
    # let a skill certify itself through its own child.

  # ── lifecycle ───────────────────────────────────────────────────────────────

  Scenario: a skill is retired when its channel stops
    Given skill "tuner/channel/ntp" was published
    When the "ntp" channel stops
    Then the skill is withdrawn from the published inventory
    And it is no longer reported to the smoke-alarm
    And a caller routing to it before withdrawal is answered with a withdrawal,
      not a stale rung

  Scenario: a nested skill outlives no child
    Given "tuner/network-health" nests "tuner/channel/ntp"
    And "tuner/channel/ntp" has been withdrawn
    When the nested skill is evaluated
    Then it is reported at rung 0 with the missing child named

  # ── publication ─────────────────────────────────────────────────────────────

  Scenario: tuner publishes the skill inventory with the fields the surface defines
    Given the smoke-alarm has certified "tuner/network-health" at rung 2
    When tuner publishes its skill inventory
    Then the entry carries skill_name, certified_rung, distance and host_instance
    And the host instance is tuner's own instance ID

  Scenario: tuner publishes only what the caller's rung permits
    Given a caller is certified at rung 1
    And "tuner/network-health" is certified at rung 3 with an isotope binding
    When the caller reads tuner's published skill inventory
    Then it sees the skill name and certified rung
    And it does not see the isotope binding or the per-hop detail

  Scenario: variation failures are reported as health, not as a rung change
    Given a nested skill returned non-equal output for equal inputs
    When tuner reports the failure
    Then it surfaces a skill-variation health event naming the skill
    And it does not change the skill's rung itself

  Scenario: the published inventory is consumable by the dashboard
    Given tuner publishes skills "tuner/channel/ntp" and "tuner/network-health"
    When adhd reads tuner's inventory alongside its own
    Then both entries appear in the same skill view
    And a skill certified by either host is shown once, at the closer distance
