// Package config loads, validates and writes the jellyfin-rpc configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// AppName is used for config/log directories.
const AppName = "jellyfin-rpc"

// Media type keys used for templates and the enabled list.
const (
	MediaEpisode    = "episode"
	MediaMovie      = "movie"
	MediaAudio      = "audio"
	MediaMusicVideo = "musicvideo"
	MediaLiveTV     = "livetv"
	MediaOther      = "other"
)

// AllMediaTypes lists every supported media type key.
var AllMediaTypes = []string{MediaEpisode, MediaMovie, MediaAudio, MediaMusicVideo, MediaLiveTV, MediaOther}

// Auth methods.
const (
	AuthPassword = "password"
	AuthAPIKey   = "apikey"
)

// Paused modes.
const (
	PausedClear = "clear"
	PausedShow  = "show"
)

// Duration is a time.Duration that (un)marshals from TOML strings like "5s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func D(v time.Duration) Duration { return Duration{v} }

type Config struct {
	Jellyfin  Jellyfin  `toml:"jellyfin"`
	Discord   Discord   `toml:"discord"`
	Behavior  Behavior  `toml:"behavior"`
	Images    Images    `toml:"images"`
	Log       Log       `toml:"log"`
	Templates Templates `toml:"templates"`
}

type Jellyfin struct {
	URL                string   `toml:"url"`
	PublicURL          string   `toml:"public_url"`
	AuthMethod         string   `toml:"auth_method"`
	APIKey             string   `toml:"api_key"`
	Token              string   `toml:"token"`
	UserID             string   `toml:"user_id"`
	Username           string   `toml:"username"`
	DeviceID           string   `toml:"device_id"`
	Timeout            Duration `toml:"timeout"`
	InsecureSkipVerify bool     `toml:"insecure_skip_verify"`
	Clients            []string `toml:"clients"`
	Devices            []string `toml:"devices"`
	IgnoreClients      []string `toml:"ignore_clients"`
	IgnoreLibraries    []string `toml:"ignore_libraries"`
}

type Discord struct {
	ClientID string `toml:"client_id"`
}

type Behavior struct {
	PollInterval      Duration `toml:"poll_interval"`
	IdlePollInterval  Duration `toml:"idle_poll_interval"`
	MaxBackoff        Duration `toml:"max_backoff"`
	MinUpdateInterval Duration `toml:"min_update_interval"`
	SeekThreshold     Duration `toml:"seek_threshold"`
	Paused            string   `toml:"paused"`
	MediaTypes        []string `toml:"media_types"`
	ShowTimestamps    bool     `toml:"show_timestamps"`
}

type Images struct {
	Enabled        bool   `toml:"enabled"`
	EpisodeSource  string `toml:"episode_source"`
	MaxHeight      int    `toml:"max_height"`
	Fallback       string `toml:"fallback"`
	PlayStateIcons bool   `toml:"play_state_icons"`
	PlayIcon       string `toml:"play_icon"`
	PauseIcon      string `toml:"pause_icon"`
}

type Log struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type Button struct {
	Label string `toml:"label"`
	URL   string `toml:"url"`
}

type Template struct {
	ActivityType  string   `toml:"activity_type"`
	StatusDisplay string   `toml:"status_display"`
	Details       string   `toml:"details"`
	DetailsURL    string   `toml:"details_url"`
	State         string   `toml:"state"`
	StateURL      string   `toml:"state_url"`
	LargeText     string   `toml:"large_text"`
	SmallText     string   `toml:"small_text"`
	Buttons       []Button `toml:"buttons"`
}

type Templates struct {
	Episode    Template `toml:"episode"`
	Movie      Template `toml:"movie"`
	Audio      Template `toml:"audio"`
	MusicVideo Template `toml:"musicvideo"`
	LiveTV     Template `toml:"livetv"`
	Other      Template `toml:"other"`
}

// For returns the template for a media type key.
func (t *Templates) For(media string) *Template {
	switch media {
	case MediaEpisode:
		return &t.Episode
	case MediaMovie:
		return &t.Movie
	case MediaAudio:
		return &t.Audio
	case MediaMusicVideo:
		return &t.MusicVideo
	case MediaLiveTV:
		return &t.LiveTV
	default:
		return &t.Other
	}
}

// Default returns a config populated with defaults.
func Default() *Config {
	return &Config{
		Jellyfin: Jellyfin{
			AuthMethod: AuthPassword,
			Timeout:    D(10 * time.Second),
		},
		Behavior: Behavior{
			PollInterval:      D(5 * time.Second),
			IdlePollInterval:  D(15 * time.Second),
			MaxBackoff:        D(60 * time.Second),
			MinUpdateInterval: D(4 * time.Second),
			SeekThreshold:     D(10 * time.Second),
			Paused:            PausedClear,
			MediaTypes:        []string{MediaEpisode, MediaMovie, MediaAudio, MediaMusicVideo, MediaLiveTV},
			ShowTimestamps:    true,
		},
		Images: Images{
			Enabled:       true,
			EpisodeSource: "series",
			MaxHeight:     512,
			Fallback:      "jellyfin",
			PlayIcon:      "play",
			PauseIcon:     "pause",
		},
		Log:       Log{Level: "info", Format: "text"},
		Templates: DefaultTemplates(),
	}
}

// DefaultTemplates returns the built-in presence templates.
func DefaultTemplates() Templates {
	imdb := Button{Label: "IMDb", URL: `{{with provider . "imdb"}}https://www.imdb.com/title/{{.}}/{{end}}`}
	return Templates{
		Episode: Template{
			ActivityType:  "watching",
			StatusDisplay: "details",
			Details:       `{{.SeriesName}}`,
			State:         `{{with epcode .}}{{.}} · {{end}}{{.Name}}`,
			LargeText:     `{{.SeriesName}}{{with .SeasonName}} · {{.}}{{end}}`,
			SmallText:     `{{if .Paused}}Paused{{else}}Playing{{end}}`,
			Buttons:       []Button{imdb},
		},
		Movie: Template{
			ActivityType:  "watching",
			StatusDisplay: "details",
			Details:       `{{.Name}}`,
			State:         `{{with .Year}}{{.}}{{end}}{{if and .Year .Genres}} · {{end}}{{join (limit .Genres 3) ", "}}`,
			LargeText:     `{{.Name}}{{with .Year}} ({{.}}){{end}}`,
			SmallText:     `{{if .Paused}}Paused{{else}}Playing{{end}}`,
			Buttons: []Button{imdb, {
				Label: "TMDB", URL: `{{with provider . "tmdb"}}https://www.themoviedb.org/movie/{{.}}{{end}}`,
			}},
		},
		Audio: Template{
			ActivityType:  "listening",
			StatusDisplay: "state",
			Details:       `{{.Name}}`,
			State:         `{{default .Artist "Unknown artist"}}`,
			LargeText:     `{{default .Album .Name}}`,
			SmallText:     `{{if .Paused}}Paused{{else}}Playing{{end}}`,
		},
		MusicVideo: Template{
			ActivityType:  "watching",
			StatusDisplay: "details",
			Details:       `{{.Name}}`,
			State:         `{{.Artist}}`,
			LargeText:     `{{.Name}}`,
			SmallText:     `{{if .Paused}}Paused{{else}}Playing{{end}}`,
		},
		LiveTV: Template{
			ActivityType:  "watching",
			StatusDisplay: "details",
			Details:       `{{.Name}}`,
			State:         `Live TV`,
			LargeText:     `{{.Name}}`,
			SmallText:     `Live`,
		},
		Other: Template{
			ActivityType:  "watching",
			StatusDisplay: "details",
			Details:       `{{.Name}}`,
			State:         `{{with .Year}}{{.}}{{end}}`,
			LargeText:     `{{.Name}}`,
			SmallText:     `{{if .Paused}}Paused{{else}}Playing{{end}}`,
		},
	}
}

// DefaultPath returns the default config file location
// ($XDG_CONFIG_HOME/jellyfin-rpc/config.toml or ~/Library/Application Support/jellyfin-rpc/config.toml).
func DefaultPath() string {
	if p := os.Getenv("JELLYFIN_RPC_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, AppName, "config.toml")
}

// ErrNotFound is returned when the config file does not exist.
var ErrNotFound = errors.New("config file not found")

// Load reads the config at path on top of defaults, applies env overrides and validates.
func Load(path string) (*Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s (run `jellyfin-rpc setup`)", ErrNotFound, path)
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown config keys in %s: %s", path, strings.Join(keys, ", "))
	}
	cfg.ApplyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// DecodeLoose decodes path onto cfg without validation (used to preserve values in setup).
func DecodeLoose(path string, cfg *Config) error {
	_, err := toml.DecodeFile(path, cfg)
	return err
}

// ApplyEnv applies JELLYFIN_RPC_* environment overrides (handy for secrets).
func (c *Config) ApplyEnv() {
	set := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	set(&c.Jellyfin.URL, "JELLYFIN_RPC_URL")
	set(&c.Jellyfin.PublicURL, "JELLYFIN_RPC_PUBLIC_URL")
	set(&c.Jellyfin.APIKey, "JELLYFIN_RPC_API_KEY")
	set(&c.Jellyfin.Token, "JELLYFIN_RPC_TOKEN")
	set(&c.Jellyfin.UserID, "JELLYFIN_RPC_USER_ID")
	set(&c.Discord.ClientID, "JELLYFIN_RPC_CLIENT_ID")
	set(&c.Log.Level, "JELLYFIN_RPC_LOG_LEVEL")
}

// Validate checks the config for errors and normalizes values.
func (c *Config) Validate() error {
	var errs []error
	c.Jellyfin.URL = strings.TrimRight(strings.TrimSpace(c.Jellyfin.URL), "/")
	c.Jellyfin.PublicURL = strings.TrimRight(strings.TrimSpace(c.Jellyfin.PublicURL), "/")
	if c.Jellyfin.URL == "" {
		errs = append(errs, errors.New("jellyfin.url is required"))
	} else if u, err := url.Parse(c.Jellyfin.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("jellyfin.url %q must be an http(s) URL", c.Jellyfin.URL))
	}
	if c.Jellyfin.PublicURL != "" {
		if u, err := url.Parse(c.Jellyfin.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("jellyfin.public_url %q must be an http(s) URL", c.Jellyfin.PublicURL))
		}
	}
	switch c.Jellyfin.AuthMethod {
	case AuthPassword:
		if c.Jellyfin.Token == "" {
			errs = append(errs, errors.New("jellyfin.token is required for auth_method=\"password\" (run `jellyfin-rpc setup`)"))
		}
	case AuthAPIKey:
		if c.Jellyfin.APIKey == "" {
			errs = append(errs, errors.New("jellyfin.api_key is required for auth_method=\"apikey\""))
		}
		if c.Jellyfin.UserID == "" && c.Jellyfin.Username == "" {
			errs = append(errs, errors.New("jellyfin.user_id or jellyfin.username is required for auth_method=\"apikey\""))
		}
	default:
		errs = append(errs, fmt.Errorf("jellyfin.auth_method must be %q or %q", AuthPassword, AuthAPIKey))
	}
	if c.Jellyfin.Timeout.Duration <= 0 {
		c.Jellyfin.Timeout = D(10 * time.Second)
	}
	if c.Discord.ClientID == "" {
		errs = append(errs, errors.New("discord.client_id is required (create an application at https://discord.com/developers/applications)"))
	}
	b := &c.Behavior
	if b.PollInterval.Duration < time.Second {
		errs = append(errs, errors.New("behavior.poll_interval must be >= 1s"))
	}
	if b.IdlePollInterval.Duration < b.PollInterval.Duration {
		b.IdlePollInterval = b.PollInterval
	}
	if b.MaxBackoff.Duration < b.PollInterval.Duration {
		b.MaxBackoff = b.PollInterval
	}
	if b.Paused != PausedClear && b.Paused != PausedShow {
		errs = append(errs, fmt.Errorf("behavior.paused must be %q or %q", PausedClear, PausedShow))
	}
	for _, m := range b.MediaTypes {
		if !slices.Contains(AllMediaTypes, m) {
			errs = append(errs, fmt.Errorf("behavior.media_types: unknown type %q (valid: %s)", m, strings.Join(AllMediaTypes, ", ")))
		}
	}
	switch c.Images.EpisodeSource {
	case "series", "season", "episode":
	default:
		errs = append(errs, errors.New(`images.episode_source must be "series", "season" or "episode"`))
	}
	for _, m := range AllMediaTypes {
		t := c.Templates.For(m)
		switch t.ActivityType {
		case "playing", "listening", "watching", "competing":
		default:
			errs = append(errs, fmt.Errorf("templates.%s.activity_type must be playing, listening, watching or competing", m))
		}
		switch t.StatusDisplay {
		case "name", "state", "details":
		default:
			errs = append(errs, fmt.Errorf("templates.%s.status_display must be name, state or details", m))
		}
		if len(t.Buttons) > 2 {
			errs = append(errs, fmt.Errorf("templates.%s: Discord allows at most 2 buttons", m))
		}
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, errors.New("log.level must be debug, info, warn or error"))
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		errs = append(errs, errors.New(`log.format must be "text" or "json"`))
	}
	return errors.Join(errs...)
}

// ImageBaseURL returns the URL Discord should use to fetch images, or "" if images are disabled.
func (c *Config) ImageBaseURL() string {
	if !c.Images.Enabled {
		return ""
	}
	return c.Jellyfin.PublicURL
}

// MediaEnabled reports whether a media type is enabled.
func (c *Config) MediaEnabled(m string) bool { return slices.Contains(c.Behavior.MediaTypes, m) }
