use chrono::{DateTime, Utc};
use serde::Deserialize;
use std::time::Duration;

#[derive(Deserialize, Debug)]
pub struct Alert {
    #[serde(default)]
    pub id: String,
    pub severity: String,
    pub triggered_at: DateTime<Utc>,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub message: String,
}

/// Severity weight matching Go implementation in signal/strength.go.
fn severity_weight(severity: &str) -> f64 {
    match severity {
        "critical" => 1.0,
        "warn" | "warning" => 0.6,
        "info" => 0.3,
        _ => 0.3,
    }
}

/// Signal strength with linear decay, matching Go formula:
/// signal = severity_weight * (1 - age/max_age)
fn strength(alert: &Alert, max_age: Duration) -> f64 {
    let age = Utc::now()
        .signed_duration_since(alert.triggered_at)
        .to_std()
        .unwrap_or(Duration::from_secs(0));

    if age > max_age {
        return 0.0;
    }

    let weight = severity_weight(&alert.severity);
    let decay = 1.0 - (age.as_secs_f64() / max_age.as_secs_f64());
    weight * decay
}

/// Color based on signal strength using ANSI 256 colors.
fn signal_color(s: f64) -> &'static str {
    if s > 0.7 {
        "\x1b[38;5;196m" // bright red (strong signal = active alert)
    } else if s > 0.4 {
        "\x1b[38;5;208m" // orange
    } else if s > 0.1 {
        "\x1b[38;5;226m" // yellow (fading)
    } else {
        "\x1b[38;5;240m" // dim gray (expired/quiet)
    }
}

const RESET: &str = "\x1b[0m";
const DIM: &str = "\x1b[2m";

/// Render signal bars to stdout.
pub fn render(json: &str, max_age: Duration, width: usize, preview: bool) {
    let alerts: Vec<Alert> = match serde_json::from_str(json) {
        Ok(a) => a,
        Err(e) => {
            eprintln!("tuner-viz: parse error: {}", e);
            return;
        }
    };

    if alerts.is_empty() {
        if preview {
            println!("{}No active alerts{}", DIM, RESET);
        }
        return;
    }

    // Calculate strengths and sort descending.
    let mut entries: Vec<(&Alert, f64)> = alerts.iter().map(|a| (a, strength(a, max_age))).collect();
    entries.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

    if preview {
        println!("Signal Strength ({} alerts)", entries.len());
        println!("{}{}{}", DIM, "─".repeat(width + 30), RESET);
    }

    for (alert, sig) in &entries {
        let filled = (*sig * width as f64).round() as usize;
        let filled = filled.min(width);
        let empty = width - filled;

        let color = signal_color(*sig);
        let bar = format!(
            "{}{}{}{}{}",
            color,
            "█".repeat(filled),
            DIM,
            "░".repeat(empty),
            RESET,
        );

        let label = if !alert.source.is_empty() {
            &alert.source
        } else if !alert.id.is_empty() {
            &alert.id
        } else {
            &alert.severity
        };

        let pct = (*sig * 100.0) as u32;
        println!(
            "  {:<20} [{}] {:>3}% {}{}{}",
            label, bar, pct, DIM, alert.severity, RESET
        );
    }
}
