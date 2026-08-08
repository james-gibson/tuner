Feature: Tuner Feature Browser — Gherkin Coverage from ADHD Cluster
  As a developer or operator using tuner
  I want to browse which Gherkin scenarios are certified by live isotope transits
  in the cluster ADHD is monitoring
  So that I can see which features are confirmed running in the system right now

  # ADHD is the source of truth for live feature certification.
  # Tuner discovers ADHD via mDNS or direct configuration, connects to its
  # MCP endpoint, and calls adhd.features.certified to get the coverage list.
  # Tuner presents this as a Television channel or inline CLI view.
  #
  # The trust rung ADHD has assigned to tuner determines which fields are
  # returned. Tuner identifies itself by instance ID; ADHD looks up its rung
  # in the registry and filters the response — tuner never declares its own rung.
  #
  # Demo vs real cluster: ADHD reports the cluster mode. Tuner labels
  # dev-mode certifications visually so the user knows which are live evidence
  # vs plaintext stand-ins.

  Background:
    Given tuner has discovered an ADHD instance via mDNS or config
    And tuner has established an MCP connection to the ADHD endpoint

  # ── discovery and connection ────────────────────────────────────────────────

  Scenario: tuner discovers an ADHD MCP endpoint via mDNS
    Given ADHD is advertising its MCP endpoint via _adhd._tcp on the local network
    When tuner browses for services
    Then it discovers the ADHD instance
    And extracts the MCP endpoint address from TXT records
    And adds the endpoint to its list of feature sources

  Scenario: tuner falls back to configured ADHD endpoint when mDNS is unavailable
    Given mDNS browsing returns no ADHD instances
    And a direct ADHD MCP endpoint is configured in tuner's config
    When tuner initialises its feature browser
    Then it connects to the configured endpoint directly

  # ── browsing certified features ─────────────────────────────────────────────

  Scenario: tuner calls adhd.features.certified and renders the feature list
    Given ADHD reports 10 features, 4 certified and 6 uncertified
    When tuner calls "adhd.features.certified"
    Then tuner displays all 10 features
    And certified features are shown with a green indicator
    And uncertified features are shown dimmed or with a dark indicator

  Scenario: tuner labels demo-mode certifications differently from real certifications
    Given ADHD is running in demo mode (cluster mode = "demo")
    And a feature is certified via a plaintext isotope
    When tuner renders the feature list
    Then the certified entry is labelled "[demo]" rather than a solid green indicator
    And a note explains "demo cluster: plaintext isotopes, not production evidence"

  Scenario: tuner shows the cluster member that provided each certification
    Given the caller (tuner) is certified at rung 3 by ADHD's registry
    And feature "adhd/mdns-discovery" was certified by "smoke:alarm-a/t1"
    When tuner calls "adhd.features.certified"
    Then the entry for "adhd/mdns-discovery" shows cluster_member="smoke:alarm-a/t1"
    And tuner renders it as "certified by alarm-a/t1"

  Scenario: tuner at rung 0 shows feature names and certified status without source details
    Given ADHD's registry holds tuner at rung 0
    When tuner calls "adhd.features.certified"
    Then feature names and certified=true/false are shown
    And no isotope IDs or cluster member names appear
    And tuner does not display a "verify" action for any feature

  # ── triggering verification ─────────────────────────────────────────────────

  Scenario: tuner at rung 3 can trigger live verification of a certified feature
    Given the feature list shows "adhd/mdns-discovery" as certified
    And tuner is at rung 3
    When the user selects "adhd/mdns-discovery" and requests verification
    Then tuner calls "adhd.features.verify" with feature "adhd/mdns-discovery"
    And ADHD probes the certifying cluster member
    And tuner renders the result: "still certified" or "certification lapsed"

  Scenario: verification result shows isotope rotation as a distinct state
    Given tuner requests verification of "adhd/mdns-discovery"
    And ADHD reports still_certified=false with "isotope rotated"
    When tuner renders the result
    Then the feature is shown in yellow with label "isotope rotated"
    And the previous and current isotope IDs are shown (if rung permits)

  Scenario: tuner below rung 3 does not offer a verify action
    Given tuner is at rung 1
    When tuner renders the feature list
    Then no "verify" button or action is available for any feature
    And a hint reads "advance to rung 3 to enable live verification"

  # ── cluster member view ─────────────────────────────────────────────────────

  Scenario: tuner can browse cluster members and their certified feature counts
    When tuner calls "adhd.features.cluster"
    Then tuner displays each cluster member with its name and mode
    And if at rung 2+, shows certified_feature_count per member
    And members with higher certified feature counts are ranked higher in the view

  Scenario: tuner shows which cluster member certifies the most features
    Given "alarm-a" certifies 6 features and "alarm-b" certifies 2 features
    When tuner renders the cluster member view
    Then "alarm-a" appears first with "6 features certified"
    And "alarm-b" appears second with "2 features certified"

  # ── Television channel integration ─────────────────────────────────────────

  Scenario: tuner generates a Television channel for ADHD's certified feature list
    Given ADHD is discovered and has certified features
    When tuner generates channels
    Then a channel named "adhd-features" is created
    And the channel source command calls "adhd.features.certified" via the MCP endpoint
    And the channel refreshes automatically when new certifications arrive

  Scenario: the adhd-features channel highlights newly certified features in real time
    Given the adhd-features TV channel is open in Television
    When a new isotope transit certifies a previously uncovered scenario
    And ADHD pushes a coverage update via its MCP SSE stream
    Then the newly certified feature transitions from dimmed to green in the channel view
    And a timestamp shows when the certification occurred

  Scenario: the adhd-features channel shows a summary row at the top
    Given 4 of 10 features are certified
    When the channel is rendered
    Then the first row shows "4 / 10 certified (40%)"
    And a progress indicator reflects the certification ratio

  # ── multi-ADHD federation ───────────────────────────────────────────────────
  # When multiple ADHD instances are running (e.g. one TUI + one headless),
  # tuner aggregates their feature coverage into a unified view.

  Scenario: tuner aggregates certified features from multiple ADHD instances
    Given tuner has discovered two ADHD instances: "adhd-tui" and "adhd-headless"
    When tuner calls "adhd.features.certified" on both
    Then tuner merges the feature lists
    And a feature certified by either instance is shown as certified
    And the cluster_member field shows which instance's cluster provided the evidence

  Scenario: a feature certified by the headless instance but not the TUI instance is still certified
    Given "adhd/headless-mode" is certified in "adhd-headless" but not in "adhd-tui"
    When tuner renders the merged feature list
    Then "adhd/headless-mode" shows certified=true
    And the source is noted as "adhd-headless"

  # ── skill certification browser ─────────────────────────────────────────────
  # Skills and features use the same certification model (existence → equality →
  # isotope). Tuner browses both surfaces from ADHD and presents them together,
  # since a certified skill may also produce the isotope that certifies a feature.

  Scenario: tuner calls adhd.skills.certified and renders the skill list alongside features
    Given ADHD reports 5 skills at various rungs
    When tuner calls "adhd.skills.certified"
    Then tuner displays a skill panel alongside the feature panel
    And each skill shows its certified_rung and a rung label:
      | rung | label           |
      | 0    | (unverified)    |
      | 1    | exists          |
      | 2    | deterministic   |
      | 3    | scenario-linked |

  Scenario: tuner shows the rung advancement path for an uncertified or low-rung skill
    Given "ghost-skill" is at rung 0 (existence not verified)
    When tuner renders the skill entry
    Then it shows the next certification step: "probe call required to verify existence"
    And if the user has rung 3, a "probe now" action is offered

  Scenario: tuner links a skill to the Gherkin scenario it certifies
    Given "open-the-pickle-jar" is isotope-certified with binding "adhd/skill-system"
    And tuner is at rung 3
    When tuner renders the skill list
    Then the "open-the-pickle-jar" entry links to the "adhd/skill-system" feature entry
    And clicking the link scrolls to or highlights that feature in the feature panel

  Scenario: skill-variation failures are visible in the tuner skill view
    Given "start-here" has 2 recorded variation failures and 42i distance 16
    When tuner renders the skill list
    Then "start-here" shows a yellow indicator with "2 variation failures"
    And the 42i distance is shown: "42i: 16"

  Scenario: tuner renders a unified "cluster health" score from feature and skill certifications
    Given the cluster has 10 features and 5 skills
    And 4 features and 3 skills are certified at rung ≥ 2
    When tuner renders the cluster health summary
    Then the summary shows: "4/10 features certified, 3/5 skills deterministic"
    And the combined 42i distance is shown as the cluster-level distance metric

  Scenario: the adhd-features TV channel includes both feature and skill certification
    When the "adhd-features" Television channel is generated
    Then the channel source returns a merged view of features and skills
    And each section is labelled: "Gherkin Features" and "Cluster Skills"
    And the overall certification ratio spans both
