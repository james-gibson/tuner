package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	ossignal "os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/james-gibson/tuner/internal/config"
	"github.com/james-gibson/tuner/internal/lezzdemo"
	"github.com/james-gibson/tuner/internal/mdns"
	"github.com/james-gibson/tuner/internal/server"
	"github.com/james-gibson/tuner/internal/signal"
	"github.com/james-gibson/tuner/internal/tv"
)

// bindWithRetry attempts to bind addr, scanning up to maxRetries higher ports
// on EADDRINUSE. Returns the listener and the address it actually bound.
func bindWithRetry(addr string, maxRetries int) (net.Listener, string, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port in %q: %w", addr, err)
	}
	for i := 0; i <= maxRetries; i++ {
		candidate := net.JoinHostPort(host, strconv.Itoa(port+i))
		ln, err := net.Listen("tcp", candidate)
		if err == nil {
			if i > 0 {
				log.Printf("port %s in use, using %s instead", addr, candidate)
			}
			return ln, candidate, nil
		}
		if !isAddrInUse(err) {
			return nil, "", fmt.Errorf("listen %s: %w", candidate, err)
		}
	}
	return nil, "", fmt.Errorf("no free port found in range %s – %s:%d", addr, host, port+maxRetries)
}

func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "bind: can't assign requested address")
}

var buildVersion string

func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	v := bi.Main.Version
	if v == "" || v == "(devel)" {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 8 {
				return s.Value[:8]
			}
		}
		return "dev"
	}
	return v
}

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		cmdServe(nil)
		return
	}

	switch args[0] {
	case "serve":
		cmdServe(args[1:])
	case "channel":
		cmdChannel(args[1:])
	case "discover":
		cmdDiscover(args[1:])
	case "list":
		cmdList(args[1:])
	case "signals":
		cmdSignals(args[1:])
	case "mdns":
		cmdMdns(args[1:])
	case "tv":
		cmdTV(args[1:])
	case "validate":
		cmdValidate(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("tuner %s\n", version())
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: tuner <command> [flags]

Commands:
  serve       Start Tuner in broadcast or receive mode
  channel     View or generate a TV channel (use --launch to open in Television)
  discover    Discover smoke-alarm services via mDNS
  list        List available channels
  signals     Monitor alert signal strength
  mdns        Real-time mDNS service monitor (for TV channel)
  tv          Manage tuner's isolated TV channels (list, launch, sync)
  validate    Validate configuration
  version     Print version

Flags:
  --config=<path>     Path to config file (optional; built-in defaults used if omitted)
  --mode=<mode>       broadcast or receive (overrides config)
  --endpoint=<url>    smoke-alarm endpoint URL (signals command only)`)
}

func cmdServe(args []string) {
	configPath := flagValue(args, "config", "")
	modeOverride := flagValue(args, "mode", "")

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}

	if modeOverride != "" {
		cfg.Mode = modeOverride
	}

	log.Printf("tuner %s starting in %s mode", version(), cfg.Mode)

	ctx, cancel := ossignal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := server.New(server.Options{
		Config:  cfg,
		Version: version(),
	})

	// Start health server — scan upward if the preferred port is occupied.
	healthLn, healthAddr, err := bindWithRetry(cfg.Health.ListenAddr, 10)
	if err != nil {
		fatal("health server: %v", err)
	}
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", srv.HandleHealthz)
	healthMux.HandleFunc("/readyz", srv.HandleReadyz)
	healthMux.HandleFunc("/status", srv.HandleStatus)
	healthSrv := &http.Server{Handler: healthMux}
	log.Printf("health server listening on %s", healthAddr)
	go func() {
		if err := healthSrv.Serve(healthLn); err != nil && err != http.ErrServerClosed {
			log.Printf("health server error: %v", err)
		}
	}()

	if cfg.Mode == config.ModeBroadcast {
		// Bind broadcast port first so we advertise the actual address via mDNS.
		broadcastLn, broadcastAddr, err := bindWithRetry(cfg.Broadcast.Listen, 10)
		if err != nil {
			fatal("broadcast server: %v", err)
		}

		// Before advertising, scan for any peers that are already running.
		// This gives the new instance immediate awareness of the existing network.
		log.Println("scanning for existing peers before advertising...")
		peerBrowser := mdns.NewBrowser(mdns.BrowserOptions{
			ServiceTypes: append(cfg.MDNS.ServiceTypes(), "_smoke-alarm._tcp"),
			Domains:      cfg.MDNS.Domains,
			Interval:     cfg.RefreshDuration(),
			OnChange: func(svc mdns.Service) {
				log.Printf("peer discovered: %s %q at %s (txt: %v)",
					svc.ServiceType, svc.Name, svc.Endpoint(), svc.TXT)
			},
		})
		scanCtx, scanCancel := context.WithTimeout(ctx, 5*time.Second)
		peerBrowser.Start(scanCtx)
		defer scanCancel()
		if peers := peerBrowser.Services(); len(peers) > 0 {
			log.Printf("found %d peer(s) already on the network", len(peers))
		} else {
			log.Println("no existing peers found — this instance is first on the network")
		}

		// Check specifically for smoke-alarm services; fall back to lezz demo registry.
		smokeAlarms := peerBrowser.ServicesByType("_smoke-alarm._tcp")
		if len(smokeAlarms) == 0 {
			if injected := lezzdemo.Seed(peerBrowser); len(injected) > 0 {
				log.Printf("smoke-alarm: mDNS found none; seeded %d from lezz demo registry", len(injected))
				smokeAlarms = injected
			}
		}
		if len(smokeAlarms) == 0 {
			log.Println("⚠️  WARNING: no smoke-alarm services discovered")
			log.Println("   preset channels will show network diagnostics only")
		}

		// mDNS advertisement uses the confirmed bound port.
		advertiser := mdns.NewAdvertiser(mdns.AdvertiserOptions{
			ServiceName: fmt.Sprintf("%s:%d", "tuner",len(peerBrowser.Services())),
			ServiceType: cfg.MDNS.ServiceType,
			Port:        mdns.ParsePort(broadcastAddr),
			TXT:         cfg.MDNS.TXTRecord,
		})
		if err := advertiser.Start(ctx); err != nil {
			log.Printf("mdns advertiser error: %v", err)
		}
		defer advertiser.Shutdown()

		broadcastMux := http.NewServeMux()
		broadcastMux.HandleFunc("/channels", srv.HandleListChannels)
		broadcastMux.HandleFunc("/channels/", srv.HandleChannel)
		broadcastMux.HandleFunc("/llms.txt", srv.HandleLLMSTxt)
		broadcastSrv := &http.Server{Handler: broadcastMux}
		log.Printf("broadcast server listening on %s", broadcastAddr)
		go func() {
			if err := broadcastSrv.Serve(broadcastLn); err != nil && err != http.ErrServerClosed {
				log.Printf("broadcast server error: %v", err)
			}
		}()

		<-ctx.Done()
		log.Println("shutting down...")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		broadcastSrv.Shutdown(shutCtx)
	} else {
		// Receive mode: start mDNS browser.
		browser := mdns.NewBrowser(mdns.BrowserOptions{
			ServiceTypes: append(cfg.MDNS.ServiceTypes(), "_smoke-alarm._tcp"),
			Domains:      cfg.MDNS.Domains,
			Interval:     cfg.RefreshDuration(),
			OnChange: func(svc mdns.Service) {
				log.Printf("discovered: %s at %s", svc.ServiceType, svc.Endpoint())
			},
		})
		go browser.Start(ctx)

		// Wait briefly then check if any smoke-alarm services were found.
		time.Sleep(6 * time.Second) // Allow initial scan to complete
		smokeAlarms := browser.ServicesByType("_smoke-alarm._tcp")
		if len(smokeAlarms) == 0 {
			if injected := lezzdemo.Seed(browser); len(injected) > 0 {
				log.Printf("smoke-alarm: mDNS found none; seeded %d from lezz demo registry", len(injected))
			} else {
				log.Println("⚠️  WARNING: no smoke-alarm services discovered on local network")
				log.Println("   Tuner is running but has no upstream data source")
				log.Println("   Ensure ocd-smoke-alarm is running with tuner.advertise=true")
			}
		}

		<-ctx.Done()
		log.Println("shutting down...")
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	healthSrv.Shutdown(shutCtx)
}

func cmdChannel(args []string) {
	if len(args) == 0 {
		fatal("usage: tuner channel <name> [--config=...] [--launch]")
	}
	name := args[0]
	configPath := flagValue(args[1:], "config", "tuner.config.yaml")
	launchFlag := flagPresent(args[1:], "launch")

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}

	// Check presets first.
	presets := tv.BuiltinPresets()
	if p, ok := presets[name]; ok {
		toml, err := tv.GenerateTOML(p)
		if err != nil {
			fatal("generate TOML: %v", err)
		}
		// Determine which cable directory to use
		cableDir := cfg.TV.CableDir
		if cfg.TV.Isolate && cfg.TV.TunerDir != "" {
			cableDir = cfg.TV.TunerDir
		}

		// Always write when auto_launch or --launch flag
		if cfg.TV.AutoLaunch || launchFlag {
			path, written, err := tv.WriteChannel(cableDir, p)
			if err != nil {
				fatal("write channel: %v", err)
			}
			if written {
				fmt.Printf("wrote channel: %s\n", path)
			} else {
				fmt.Printf("channel unchanged: %s\n", path)
			}

			// Launch Television if --launch flag specified
			if launchFlag {
				fmt.Println("launching television...")
				// Pass cable dir for isolation if enabled
				launchDir := ""
				if cfg.TV.Isolate {
					launchDir = cableDir
				}
				if err := tv.LaunchTelevision(name, launchDir); err != nil {
					fatal("launch television: %v", err)
				}
			}
		} else {
			fmt.Print(toml)
		}
		return
	}

	// Check custom channels.
	for _, c := range cfg.Channels.Custom {
		if c.Name == name {
			fmt.Printf("custom channel: %s (source: %s)\n", c.Name, c.Source.Type)
			return
		}
	}

	fatal("unknown channel: %s", name)
}

func cmdDiscover(args []string) {
	configPath := flagValue(args, "config", "")
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}

	fmt.Println("discovering services via mDNS...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	browser := mdns.NewBrowser(mdns.BrowserOptions{
		ServiceTypes: append(cfg.MDNS.ServiceTypes(), "_smoke-alarm._tcp"),
		Domains:      cfg.MDNS.Domains,
		Interval:     5 * time.Second,
		OnChange: func(svc mdns.Service) {
			fmt.Printf("  found: %s at %s (txt: %v)\n", svc.ServiceType, svc.Endpoint(), svc.TXT)
		},
	})
	browser.Start(ctx)

	services := browser.Services()
	if len(services) == 0 {
		fmt.Println("no services found — ensure other tuner or smoke-alarm instances are running on the local network")
	} else {
		fmt.Printf("found %d service(s):\n", len(services))
		for _, svc := range services {
			fmt.Printf("  %s %q at %s (txt: %v)\n", svc.ServiceType, svc.Name, svc.Endpoint(), svc.TXT)
		}
	}
}

func cmdList(_ []string) {
	configPath := flagValue(os.Args[2:], "config", "")
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	fmt.Println("Available channels:")
	for _, p := range cfg.Channels.Preset {
		fmt.Printf("  [preset] %s\n", p.Name)
	}
	for _, c := range cfg.Channels.Custom {
		fmt.Printf("  [custom] %s (%s)\n", c.Name, c.Source.Type)
	}
}

func cmdSignals(args []string) {
	configPath := flagValue(args, "config", "")
	endpoint := flagValue(args, "endpoint", "")

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}

	// If no endpoint specified, try to discover one via mDNS
	if endpoint == "" {
		fmt.Println("discovering smoke-alarm services...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		browser := mdns.NewBrowser(mdns.BrowserOptions{
			ServiceTypes: []string{"_smoke-alarm._tcp"},
			Domains:      []string{"local"},
			Interval:     5 * time.Second,
		})
		browser.Start(ctx)

		services := browser.ServicesByType("_smoke-alarm._tcp")
		if len(services) == 0 {
			services = lezzdemo.SmokeAlarms()
		}
		if len(services) == 0 {
			fatal("no smoke-alarm services found. Use --endpoint=<url> to specify manually.")
		}
		endpoint = services[0].Endpoint()
		fmt.Printf("using: %s\n", endpoint)
	}

	// Fetch status
	resp, err := http.Get(endpoint + "/status")
	if err != nil {
		fatal("fetch status: %v", err)
	}
	defer resp.Body.Close()

	var status struct {
		Alerts []signal.Alert `json:"alerts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		fatal("decode status: %v", err)
	}

	// Display signal bars
	maxAge := cfg.MaxAgeDuration()
	fmt.Printf("\nSignal Strength (max age: %s)\n", maxAge)
	fmt.Println(strings.Repeat("─", 50))

	for _, alert := range status.Alerts {
		str := signal.Strength(alert, maxAge)
		bar := signal.VizBar(str, cfg.Signal.VizBars)
		fmt.Printf("  %-20s [%s] %.0f%% %s\n",
			alert.Source, bar, str*100, alert.Severity)
	}

	if len(status.Alerts) == 0 {
		fmt.Println("  No active alerts")
	}
}

func cmdMdns(args []string) {
	// Parse flags
	duration := flagValue(args, "duration", "8s")
	serviceTypes := serviceTypes(args)
	format := flagValue(args, "format", "line") // line or json

	scanDuration, err := time.ParseDuration(duration)
	if err != nil {
		scanDuration = 8 * time.Second
	}

	// Parse service types
	types := strings.Split(serviceTypes, ",")
	for i := range types {
		types[i] = strings.TrimSpace(types[i])
	}

	ctx, cancel := context.WithTimeout(context.Background(), scanDuration)
	defer cancel()

	// Track services as they're discovered for real-time output
	seen := make(map[string]bool)
	var mu sync.Mutex

	browser := mdns.NewBrowser(mdns.BrowserOptions{
		ServiceTypes: types,
		Domains:      []string{"local"},
		Interval:     1 * time.Second, // Scan frequently to catch new services
		Quiet:        true,            // Suppress logs for clean TV output
		OnChange: func(svc mdns.Service) {
			mu.Lock()
			key := fmt.Sprintf("%s:%s:%d", svc.ServiceType, svc.Host, svc.Port)
			if seen[key] {
				mu.Unlock()
				return
			}
			seen[key] = true
			mu.Unlock()

			if format == "json" {
				data, _ := json.Marshal(svc)
				fmt.Println(string(data))
			} else {
				// Format: TYPE HOST:PORT NAME [txt]
				txt := ""
				if len(svc.TXT) > 0 {
					parts := make([]string, 0, len(svc.TXT))
					for k, v := range svc.TXT {
						parts = append(parts, fmt.Sprintf("%s=%s", k, v))
					}
					txt = " [" + strings.Join(parts, ", ") + "]"
				}
				// Timestamp for real-time feel
				ts := time.Now().Format("15:04:05")
				fmt.Printf("%s %-20s %s:%-5d %s%s\n",
					ts, svc.ServiceType, svc.Host, svc.Port, svc.Name, txt)
			}
		},
	})

	// Run synchronously - Start blocks until context is done
	go browser.Start(ctx)

	// Wait for the full scan duration
	<-ctx.Done()

	// Output final summary to stderr so it doesn't interfere with TV parsing
	services := browser.Services()
	fmt.Fprintf(os.Stderr, "# discovered %d service(s)\n", len(services))
}

func serviceTypes(args []string) string {
	serviceTypes := flagValue(args, "services", "_smoke-alarm._tcp,_tuner._tcp,_http._tcp,_https._tcp,_ssh._tcp,_smb._tcp,_afpovertcp._tcp,_airplay._tcp,_raop._tcp,_homekit._tcp,_googlecast._tcp,_spotify-connect._tcp")
	fmt.Println(string(serviceTypes))
	return serviceTypes
}

func cmdTV(args []string) {
	configPath := flagValue(args, "config", "")

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}

	// Determine cable directory
	cableDir := cfg.TV.TunerDir
	if cableDir == "" {
		cableDir = cfg.TV.CableDir
	}
	expandedDir := expandHome(cableDir)

	// Subcommands: list, launch, sync, open
	subcmd := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		subcmd = args[0]
		args = args[1:]
	}

	switch subcmd {
	case "list":
		// List available tuner channels
		fmt.Printf("Tuner Cable Directory: %s\n", expandedDir)
		fmt.Printf("Isolation: %v\n\n", cfg.TV.Isolate)

		// Check if directory exists
		entries, err := os.ReadDir(expandedDir)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No channels yet. Use 'tuner channel <name>' to create one.")
				fmt.Println("\nAvailable presets:")
				for name, p := range tv.BuiltinPresets() {
					fmt.Printf("  %-12s %s\n", name, p.Description)
				}
				return
			}
			fatal("read cable dir: %v", err)
		}

		fmt.Println("Channels:")
		count := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".toml") {
				name := strings.TrimSuffix(e.Name(), ".toml")
				name = strings.TrimPrefix(name, "ocd-")
				fmt.Printf("  %s\n", name)
				count++
			}
		}
		if count == 0 {
			fmt.Println("  (none)")
		}

		fmt.Println("\nAvailable presets:")
		for name, p := range tv.BuiltinPresets() {
			fmt.Printf("  %-12s %s\n", name, p.Description)
		}

	case "launch", "open":
		// Launch TV with isolated cable directory
		channelName := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
			channelName = args[0]
		}

		// If no channel specified, show picker of tuner channels only
		if channelName == "" {
			channels := listTunerChannels(expandedDir)
			if len(channels) == 0 {
				fmt.Println("No tuner channels available. Run 'tuner tv sync' first.")
				return
			}

			fmt.Println("Tuner Channels (isolated from TV defaults):")
			fmt.Println()
			for i, ch := range channels {
				fmt.Printf("  [%d] %s\n", i+1, ch)
			}
			fmt.Println()
			fmt.Print("Select channel (number or name): ")

			var input string
			fmt.Scanln(&input)
			input = strings.TrimSpace(input)

			// Check if numeric
			if num, err := strconv.Atoi(input); err == nil && num >= 1 && num <= len(channels) {
				channelName = channels[num-1]
			} else {
				// Try to match by name
				for _, ch := range channels {
					if strings.EqualFold(ch, input) || strings.EqualFold(strings.TrimPrefix(ch, "ocd-"), input) {
						channelName = ch
						break
					}
				}
			}

			if channelName == "" {
				fatal("invalid selection: %s", input)
			}
		}

		fmt.Printf("Launching: %s (cable: %s)\n", channelName, expandedDir)
		launchDir := ""
		if cfg.TV.Isolate {
			launchDir = cableDir
		}
		if err := tv.LaunchTelevision(channelName, launchDir); err != nil {
			fatal("launch television: %v", err)
		}

	case "sync":
		// Write all preset channels to the cable directory
		fmt.Printf("Syncing presets to: %s\n", expandedDir)
		paths, written, err := tv.WriteAllPresets(cableDir)
		if err != nil {
			fatal("sync presets: %v", err)
		}
		fmt.Printf("Synced %d channels (%d written, %d unchanged)\n",
			len(paths), written, len(paths)-written)
		for _, p := range paths {
			fmt.Printf("  %s\n", p)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown tv subcommand: %s\n", subcmd)
		fmt.Fprintln(os.Stderr, "usage: tuner tv [list|launch|sync] [--config=...]")
		fmt.Fprintln(os.Stderr, "  list    List tuner-managed channels")
		fmt.Fprintln(os.Stderr, "  launch  Open Television with tuner channels only")
		fmt.Fprintln(os.Stderr, "  sync    Write all preset channels to cable directory")
		os.Exit(1)
	}
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func listTunerChannels(cableDir string) []string {
	entries, err := os.ReadDir(cableDir)
	if err != nil {
		return nil
	}

	var channels []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".toml") {
			name := strings.TrimSuffix(e.Name(), ".toml")
			// Remove ocd- prefix for display but keep for launch
			displayName := strings.TrimPrefix(name, "ocd-")
			channels = append(channels, displayName)
		}
	}
	return channels
}

func cmdValidate(args []string) {
	configPath := flagValue(args, "config", "")
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("config error: %v", err)
	}
	fmt.Printf("config OK: version=%s mode=%s channels=%d\n",
		cfg.Version, cfg.Mode,
		len(cfg.Channels.Preset)+len(cfg.Channels.Custom))
}

func flagValue(args []string, name, fallback string) string {
	prefix := "--" + name + "="
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return fallback
}

func flagPresent(args []string, name string) bool {
	flag := "--" + name
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
