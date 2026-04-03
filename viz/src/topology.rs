use serde::Deserialize;

#[derive(Deserialize, Debug)]
pub struct MembershipSnapshot {
    #[serde(default)]
    pub introducer: String,
    #[serde(default)]
    pub peers: Vec<PeerRecord>,
    #[serde(default)]
    pub version: u64,
}

#[derive(Deserialize, Debug)]
pub struct PeerRecord {
    pub id: String,
    #[serde(default)]
    pub service_name: String,
    #[serde(default)]
    pub hostname: String,
    #[serde(default)]
    pub port: u16,
    #[serde(default)]
    pub role: String,
    #[serde(default)]
    pub introducer: String,
}

const GREEN: &str = "\x1b[32m";
const YELLOW: &str = "\x1b[33m";
const BOLD: &str = "\x1b[1m";
const DIM: &str = "\x1b[2m";
const RESET: &str = "\x1b[0m";

/// Render federation topology to stdout.
pub fn render(json: &str, self_id: Option<&str>) {
    let snapshot: MembershipSnapshot = match serde_json::from_str(json) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("tuner-viz: parse topology: {}", e);
            return;
        }
    };

    println!(
        "Federation Topology {}(v{}){}",
        DIM, snapshot.version, RESET
    );
    println!("{}{}{}", DIM, "─".repeat(50), RESET);

    if snapshot.peers.is_empty() {
        println!("  {}No peers discovered{}", DIM, RESET);
        return;
    }

    for peer in &snapshot.peers {
        let is_self = self_id.map_or(false, |id| id == peer.id);
        let is_introducer = peer.role == "introducer";

        let role_icon = if is_introducer { "★" } else { "●" };
        let color = if is_introducer { GREEN } else { YELLOW };
        let highlight = if is_self {
            format!("{}{}", BOLD, color)
        } else {
            color.to_string()
        };

        let self_marker = if is_self { " ← you" } else { "" };
        let short_id = &peer.id[..8.min(peer.id.len())];

        println!(
            "  {}{} {:<12} {}:{}  [{}]{}{}",
            highlight, role_icon, peer.role, peer.hostname, peer.port, short_id, self_marker, RESET,
        );

        // Show connection to introducer if follower.
        if !is_introducer && !peer.introducer.is_empty() {
            let intro_short = &peer.introducer[..8.min(peer.introducer.len())];
            println!("    {}└─→ introducer {}{}", DIM, intro_short, RESET);
        }
    }

    let intro_short = &snapshot.introducer[..8.min(snapshot.introducer.len())];
    println!(
        "\n  {}Peers: {} | Introducer: {}{}",
        DIM,
        snapshot.peers.len(),
        intro_short,
        RESET,
    );
}
