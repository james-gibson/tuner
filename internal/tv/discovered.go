// TV channel generation for discovered sources. Presets are written by
// `tuner tv sync`; this file is the other half, where a smoke-alarm instance
// discovered while tuner serves gets a channel of its own
// (features/tuner-mdns-continuous.feature).
package tv

import (
	"fmt"
	"log"

	"github.com/james-gibson/tuner/internal/channelsource"
)

// DiscoveredPreset renders a discovered source as a TV channel. The source
// command points at the instance's own /status, so the channel shows that
// instance's current health rather than a shared aggregate.
//
// It goes through `tuner signals`, not `curl`, for the same reason the signal
// preset does: the endpoint serves a /status document (targets), and the raw
// document is not what any renderer wants (tuner#23).
func DiscoveredPreset(src channelsource.Source) Preset {
	command, preview := SignalCommands(src.Endpoint)
	return Preset{
		Name:        src.Instance,
		Description: fmt.Sprintf("Discovered smoke-alarm %s", src.Instance),
		Command:     command,
		Watch:       5.0,
		Preview:     preview,
	}
}

// WatchDiscovered writes one TOML file per discovered channel source and
// removes it when the source retires. It returns an unsubscribe function.
//
// Only the file for the instance that changed is touched: presets and the
// other discovered channels are left exactly as they were, and WriteChannel
// skips a file whose content is already correct, so a redelivered
// announcement does not rewrite anything.
//
// A discovered instance that would shadow a built-in preset is skipped: the
// cable directory is shared, and a smoke-alarm calling itself "ntp" must not
// replace tuner's NTP channel.
func WatchDiscovered(registry *channelsource.Registry, cableDir string) func() {
	if registry == nil || cableDir == "" {
		return func() {}
	}
	presets := BuiltinPresets()

	return registry.Subscribe(func(event channelsource.Event) {
		src := event.Source
		if event.Kind == channelsource.Retired {
			path, removed, err := RemoveChannel(cableDir, src.Instance)
			if err != nil {
				log.Printf("tv: remove channel for %q: %v", src.Instance, err)
				return
			}
			if removed {
				log.Printf("tv: removed channel %s (source %s departed)", path, src.Instance)
			}
			return
		}

		if _, clash := presets[src.Instance]; clash {
			log.Printf("tv: discovered instance %q shadows a preset channel, not writing a TOML", src.Instance)
			return
		}

		path, written, err := WriteChannel(cableDir, DiscoveredPreset(src))
		if err != nil {
			log.Printf("tv: write channel for %q: %v", src.Instance, err)
			return
		}
		if written {
			log.Printf("tv: wrote channel %s for %s at %s", path, src.Instance, src.Endpoint)
		}
	})
}
