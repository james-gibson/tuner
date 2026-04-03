use clap::{Parser, Subcommand};
use std::io::{self, Read};
use std::time::Duration;

mod signal;
mod topology;

#[derive(Parser)]
#[command(name = "tuner-viz", version, about = "Terminal visualization for Tuner")]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Render signal strength bars from JSON alert data
    Signal {
        /// Read JSON from stdin instead of args
        #[arg(long)]
        stdin: bool,

        /// JSON array of alerts (if not using --stdin)
        #[arg(long)]
        data: Option<String>,

        /// Maximum alert age in minutes (default: 30)
        #[arg(long, default_value = "30")]
        max_age: u64,

        /// Number of bar characters (default: 16)
        #[arg(long, default_value = "16")]
        width: usize,

        /// Render for Television preview pane
        #[arg(long)]
        preview: bool,
    },

    /// Render federation topology from membership JSON
    Topology {
        /// Read JSON from stdin
        #[arg(long)]
        stdin: bool,

        /// JSON membership data
        #[arg(long)]
        data: Option<String>,

        /// Highlight this instance ID
        #[arg(long)]
        self_id: Option<String>,
    },

    /// Check if tuner-viz is available (for fallback detection)
    Ping,
}

fn main() {
    let cli = Cli::parse();

    match cli.command {
        Commands::Signal {
            stdin,
            data,
            max_age,
            width,
            preview,
        } => {
            let json = read_input(stdin, data);
            let max_age = Duration::from_secs(max_age * 60);
            signal::render(&json, max_age, width, preview);
        }
        Commands::Topology {
            stdin,
            data,
            self_id,
        } => {
            let json = read_input(stdin, data);
            topology::render(&json, self_id.as_deref());
        }
        Commands::Ping => {
            println!("tuner-viz ok");
        }
    }
}

fn read_input(stdin: bool, data: Option<String>) -> String {
    if stdin {
        let mut buf = String::new();
        io::stdin().read_to_string(&mut buf).unwrap_or_default();
        buf
    } else {
        data.unwrap_or_else(|| "[]".to_string())
    }
}
