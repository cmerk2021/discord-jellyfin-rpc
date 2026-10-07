package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// tomlStr quotes s as a TOML basic string.
func tomlStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlList(v []string) string {
	q := make([]string, len(v))
	for i, s := range v {
		q[i] = tomlStr(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

var fileTmpl = template.Must(template.New("config").Delims("<%", "%>").Funcs(template.FuncMap{
	"q":    tomlStr,
	"list": tomlList,
}).Parse(fileTemplate))

// Render renders the config as a commented TOML document.
func (c *Config) Render() ([]byte, error) {
	var buf bytes.Buffer
	type tmplEntry struct {
		Key string
		T   *Template
	}
	var ts []tmplEntry
	for _, m := range AllMediaTypes {
		ts = append(ts, tmplEntry{m, c.Templates.For(m)})
	}
	err := fileTmpl.Execute(&buf, struct {
		*Config
		TemplateList []tmplEntry
	}{c, ts})
	return buf.Bytes(), err
}

// Save writes the config atomically with 0600 permissions.
func (c *Config) Save(path string) error {
	data, err := c.Render()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const fileTemplate = `# jellyfin-rpc configuration
# Docs: https://github.com/cmerk2021/discord-jellyfin-rpc#configuration
# Reload a running instance with: systemctl --user reload jellyfin-rpc (or restart it)

[jellyfin]
# Address jellyfin-rpc uses to talk to your server (LAN address is fine).
url = <% q .Jellyfin.URL %>
# Address Discord uses to download cover art. Must be reachable from the internet.
# Leave empty to disable cover art and use the [images] fallback asset instead.
public_url = <% q .Jellyfin.PublicURL %>
# "password" (token obtained by logging in during setup) or "apikey" (admin-created API key).
auth_method = <% q .Jellyfin.AuthMethod %>
api_key = <% q .Jellyfin.APIKey %>
token = <% q .Jellyfin.Token %>
# Whose playback to show. user_id takes precedence over username.
user_id = <% q .Jellyfin.UserID %>
username = <% q .Jellyfin.Username %>
# Stable identifier sent to Jellyfin; don't change it unless you want a new device entry.
device_id = <% q .Jellyfin.DeviceID %>
timeout = <% q .Jellyfin.Timeout.String %>
insecure_skip_verify = <% .Jellyfin.InsecureSkipVerify %>
# Only show sessions from these client names (e.g. "Jellyfin Web", "Jellyfin Media Player"). Empty = all.
clients = <% list .Jellyfin.Clients %>
# Only show sessions from these device names. Empty = all.
devices = <% list .Jellyfin.Devices %>
# Never show sessions from these client names.
ignore_clients = <% list .Jellyfin.IgnoreClients %>
# Never show items from these libraries (by library name).
ignore_libraries = <% list .Jellyfin.IgnoreLibraries %>

[discord]
# Discord application ID. The activity's app name is only shown when status_display = "name".
client_id = <% q .Discord.ClientID %>

[behavior]
# How often to poll Jellyfin while something is playing / while idle.
poll_interval = <% q .Behavior.PollInterval.String %>
idle_poll_interval = <% q .Behavior.IdlePollInterval.String %>
# Upper bound for retry backoff when Jellyfin or Discord is unreachable.
max_backoff = <% q .Behavior.MaxBackoff.String %>
# Minimum time between presence updates (Discord rate-limits activity updates).
min_update_interval = <% q .Behavior.MinUpdateInterval.String %>
# Re-sync the progress bar if the position drifts by more than this (e.g. after seeking).
seek_threshold = <% q .Behavior.SeekThreshold.String %>
# What to do when playback is paused: "clear" the presence or "show" it without a progress bar.
paused = <% q .Behavior.Paused %>
# Media types to show: episode, movie, audio, musicvideo, livetv, other
media_types = <% list .Behavior.MediaTypes %>
# Show the progress bar / elapsed time.
show_timestamps = <% .Behavior.ShowTimestamps %>

[images]
# Use Jellyfin artwork (requires jellyfin.public_url).
enabled = <% .Images.Enabled %>
# Artwork used for episodes: "series", "season" or "episode".
episode_source = <% q .Images.EpisodeSource %>
max_height = <% .Images.MaxHeight %>
# Discord asset key (uploaded to your Discord application) or URL used when artwork is unavailable.
fallback = <% q .Images.Fallback %>
# Show a small play/pause icon (asset keys must exist in your Discord application).
play_state_icons = <% .Images.PlayStateIcons %>
play_icon = <% q .Images.PlayIcon %>
pause_icon = <% q .Images.PauseIcon %>

[log]
level = <% q .Log.Level %>   # debug, info, warn, error
format = <% q .Log.Format %> # text, json

# ---------------------------------------------------------------------------
# Templates (Go text/template syntax: https://pkg.go.dev/text/template)
#
# activity_type:  playing | listening | watching | competing
# status_display: what your status line shows, e.g. "Watching <x>":
#                 "name" (app name), "details" or "state"
# details/state:  first/second line. Empty result = line hidden.
# buttons:        up to 2 {label, url}; a button is skipped when its url renders empty.
#
# Available fields: .Type .MediaType .Name .OriginalTitle .SeriesName .SeasonName .Season
#   .Episode .EpisodeEnd .HasSeason .HasEpisode .Year .Album .Artist .Artists .AlbumArtist
#   .Genres .Studios .OfficialRating .CommunityRating .Paused .Position .Runtime
#   .Providers .ItemID .SeriesID .User .Client .Device .ServerURL .WebURL
# Functions: epcode pad pad3 provider join limit default trunc upper lower title
#   duration (e.g. {{duration .Position}}) percent
# ---------------------------------------------------------------------------
<%- range .TemplateList %>

[templates.<% .Key %>]
activity_type = <% q .T.ActivityType %>
status_display = <% q .T.StatusDisplay %>
details = <% q .T.Details %>
details_url = <% q .T.DetailsURL %>
state = <% q .T.State %>
state_url = <% q .T.StateURL %>
large_text = <% q .T.LargeText %>
small_text = <% q .T.SmallText %>
<%- if not .T.Buttons %>
buttons = []
<%- end %>
<%- $key := .Key %>
<%- range .T.Buttons %>

[[templates.<% $key %>.buttons]]
label = <% q .Label %>
url = <% q .URL %>
<%- end %>
<%- end %>
`
