// Package presence turns Jellyfin sessions into Discord activities.
package presence

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/discord"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/jellyfin"
)

// Discord field limits.
const (
	maxText        = 128
	minText        = 2
	maxButtonLabel = 32
	maxURL         = 256
)

// Data is the value passed to templates.
type Data struct {
	Type            string // Jellyfin item type, e.g. "Episode"
	MediaType       string // jellyfin-rpc media key, e.g. "episode"
	Name            string
	OriginalTitle   string
	SeriesName      string
	SeasonName      string
	Season          int
	Episode         int
	EpisodeEnd      int
	HasSeason       bool
	HasEpisode      bool
	Year            int
	Album           string
	Artist          string
	Artists         []string
	AlbumArtist     string
	Genres          []string
	Studios         []string
	OfficialRating  string
	CommunityRating float64
	Paused          bool
	Position        time.Duration
	Runtime         time.Duration
	Providers       map[string]string
	ItemID          string
	SeriesID        string
	User            string
	Client          string
	Device          string
	ServerURL       string
	WebURL          string
}

// MediaKey maps a Jellyfin item type to a jellyfin-rpc media type key.
func MediaKey(itemType string) string {
	switch itemType {
	case "Episode":
		return config.MediaEpisode
	case "Movie":
		return config.MediaMovie
	case "Audio", "AudioBook":
		return config.MediaAudio
	case "MusicVideo":
		return config.MediaMusicVideo
	case "TvChannel", "LiveTvChannel", "LiveTvProgram", "Program":
		return config.MediaLiveTV
	default:
		return config.MediaOther
	}
}

// NewData builds template data from a session.
func NewData(s *jellyfin.Session, publicURL string) Data {
	it := s.NowPlayingItem
	d := Data{
		Type:            it.Type,
		MediaType:       MediaKey(it.Type),
		Name:            it.Name,
		OriginalTitle:   it.OriginalTitle,
		SeriesName:      it.SeriesName,
		SeasonName:      it.SeasonName,
		Year:            it.ProductionYear,
		Album:           it.Album,
		Artists:         it.Artists,
		AlbumArtist:     it.AlbumArtist,
		Genres:          it.Genres,
		OfficialRating:  it.OfficialRating,
		CommunityRating: it.CommunityRating,
		Paused:          s.PlayState.IsPaused,
		Position:        jellyfin.TicksToDuration(s.PlayState.PositionTicks),
		Runtime:         jellyfin.TicksToDuration(it.RunTimeTicks),
		Providers:       it.ProviderIDs,
		ItemID:          it.ID,
		SeriesID:        it.SeriesID,
		User:            s.UserName,
		Client:          s.Client,
		Device:          s.DeviceName,
		ServerURL:       publicURL,
	}
	if it.ParentIndexNumber != nil {
		d.Season, d.HasSeason = *it.ParentIndexNumber, true
	}
	if it.IndexNumber != nil {
		d.Episode, d.HasEpisode = *it.IndexNumber, true
	}
	if it.IndexNumberEnd != nil && d.HasEpisode && *it.IndexNumberEnd > d.Episode {
		d.EpisodeEnd = *it.IndexNumberEnd
	}
	switch {
	case len(it.Artists) > 0:
		d.Artist = strings.Join(it.Artists, ", ")
	case it.AlbumArtist != "":
		d.Artist = it.AlbumArtist
	case len(it.AlbumArtists) > 0:
		d.Artist = it.AlbumArtists[0].Name
	}
	if d.AlbumArtist == "" && len(it.AlbumArtists) > 0 {
		d.AlbumArtist = it.AlbumArtists[0].Name
	}
	for _, st := range it.Studios {
		d.Studios = append(d.Studios, st.Name)
	}
	if d.MediaType == config.MediaLiveTV && d.Name == "" {
		d.Name = it.ChannelName
	}
	if publicURL != "" && it.ID != "" {
		d.WebURL = strings.TrimRight(publicURL, "/") + "/web/#/details?id=" + it.ID
	}
	return d
}

func epcode(d Data) string {
	var b strings.Builder
	if d.HasSeason {
		fmt.Fprintf(&b, "S%02d", d.Season)
	}
	if d.HasEpisode {
		fmt.Fprintf(&b, "E%02d", d.Episode)
		if d.EpisodeEnd > 0 {
			fmt.Fprintf(&b, "-E%02d", d.EpisodeEnd)
		}
	}
	return b.String()
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case int:
		return x == 0
	case float64:
		return x == 0
	case []string:
		return len(x) == 0
	case bool:
		return !x
	}
	return false
}

// FuncMap holds the helper functions available to templates.
var FuncMap = template.FuncMap{
	"epcode": epcode,
	"pad":    func(v any) string { return fmt.Sprintf("%02d", toInt(v)) },
	"pad3":   func(v any) string { return fmt.Sprintf("%03d", toInt(v)) },
	"provider": func(d Data, name string) string {
		for k, v := range d.Providers {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	},
	"join": func(v []string, sep string) string { return strings.Join(v, sep) },
	"limit": func(v []string, n int) []string {
		if len(v) > n {
			return v[:n]
		}
		return v
	},
	"default": func(v any, def any) any {
		if isEmpty(v) {
			return def
		}
		return v
	},
	"trunc": truncate,
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"title": func(s string) string {
		words := strings.Fields(s)
		for i, w := range words {
			r, n := utf8.DecodeRuneInString(w)
			words[i] = strings.ToUpper(string(r)) + w[n:]
		}
		return strings.Join(words, " ")
	},
	"duration": formatDuration,
	"percent": func(pos, total time.Duration) int {
		if total <= 0 {
			return 0
		}
		return int(math.Round(math.Max(0, math.Min(1, float64(pos)/float64(total))) * 100))
	},
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func truncate(n int, s string) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n <= 1 {
		return string(r[:n])
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

type compiled struct {
	src           *config.Template
	activityType  int
	statusDisplay int
	details       *template.Template
	detailsURL    *template.Template
	state         *template.Template
	stateURL      *template.Template
	largeText     *template.Template
	smallText     *template.Template
	buttons       [][2]*template.Template
}

// Renderer renders activities from sessions using the configured templates.
type Renderer struct {
	cfg       *config.Config
	templates map[string]*compiled
}

// NewRenderer compiles all templates, returning an error that names the bad template.
func NewRenderer(cfg *config.Config) (*Renderer, error) {
	r := &Renderer{cfg: cfg, templates: map[string]*compiled{}}
	for _, m := range config.AllMediaTypes {
		t := cfg.Templates.For(m)
		c := &compiled{src: t}
		var err error
		parse := func(field, src string) *template.Template {
			if err != nil {
				return nil
			}
			var tt *template.Template
			tt, err = template.New(m + "." + field).Funcs(FuncMap).Option("missingkey=zero").Parse(src)
			if err != nil {
				err = fmt.Errorf("templates.%s.%s: %w", m, field, err)
			}
			return tt
		}
		c.details = parse("details", t.Details)
		c.detailsURL = parse("details_url", t.DetailsURL)
		c.state = parse("state", t.State)
		c.stateURL = parse("state_url", t.StateURL)
		c.largeText = parse("large_text", t.LargeText)
		c.smallText = parse("small_text", t.SmallText)
		for i, b := range t.Buttons {
			c.buttons = append(c.buttons, [2]*template.Template{
				parse(fmt.Sprintf("buttons[%d].label", i), b.Label),
				parse(fmt.Sprintf("buttons[%d].url", i), b.URL),
			})
		}
		if err != nil {
			return nil, err
		}
		c.activityType = activityType(t.ActivityType)
		c.statusDisplay = statusDisplay(t.StatusDisplay)
		r.templates[m] = c
	}
	return r, nil
}

func activityType(s string) int {
	switch s {
	case "playing":
		return discord.ActivityPlaying
	case "listening":
		return discord.ActivityListening
	case "competing":
		return discord.ActivityCompeting
	default:
		return discord.ActivityWatching
	}
}

func statusDisplay(s string) int {
	switch s {
	case "state":
		return discord.StatusDisplayState
	case "details":
		return discord.StatusDisplayDetails
	default:
		return discord.StatusDisplayName
	}
}

func exec(t *template.Template, d Data) (string, error) {
	if t == nil {
		return "", nil
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(buf.String()), " "), nil
}

// fitText enforces Discord's 2..128 character limits; empty stays empty.
func fitText(s string) string {
	if s == "" {
		return ""
	}
	s = truncate(maxText, s)
	if utf8.RuneCountInString(s) < minText {
		s += "\u200b"
	}
	return s
}

func validURL(s string) bool {
	return len(s) <= maxURL && (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://"))
}

// Render builds the activity for a session at time now.
func (r *Renderer) Render(s *jellyfin.Session, now time.Time) (*discord.Activity, error) {
	d := NewData(s, r.cfg.Jellyfin.PublicURL)
	c := r.templates[d.MediaType]
	var errs []error
	run := func(t *template.Template) string {
		out, err := exec(t, d)
		if err != nil {
			errs = append(errs, err)
		}
		return out
	}
	a := &discord.Activity{
		Type:              c.activityType,
		StatusDisplayType: c.statusDisplay,
		Details:           fitText(run(c.details)),
		State:             fitText(run(c.state)),
	}
	if u := run(c.detailsURL); validURL(u) && a.Details != "" {
		a.DetailsURL = u
	}
	if u := run(c.stateURL); validURL(u) && a.State != "" {
		a.StateURL = u
	}
	// Discord requires details or state for the status display to be useful; fall back to the name.
	if a.Details == "" && a.State == "" {
		a.Details = fitText(d.Name)
	}
	assets := &discord.Assets{LargeText: fitText(run(c.largeText))}
	assets.LargeImage = r.imageURL(s.NowPlayingItem)
	if assets.LargeImage == "" {
		assets.LargeImage = r.cfg.Images.Fallback
	}
	if r.cfg.Images.PlayStateIcons {
		if d.Paused {
			assets.SmallImage = r.cfg.Images.PauseIcon
		} else {
			assets.SmallImage = r.cfg.Images.PlayIcon
		}
		if assets.SmallImage != "" {
			assets.SmallText = fitText(run(c.smallText))
		}
	}
	if assets.LargeImage == "" {
		assets.LargeText = ""
	}
	if *assets != (discord.Assets{}) {
		a.Assets = assets
	}
	for _, b := range c.buttons {
		label := truncate(maxButtonLabel, run(b[0]))
		u := run(b[1])
		if label == "" || !validURL(u) {
			continue
		}
		a.Buttons = append(a.Buttons, discord.Button{Label: label, URL: u})
	}
	if r.cfg.Behavior.ShowTimestamps && !d.Paused {
		start := now.Add(-d.Position)
		ts := &discord.Timestamps{Start: start.UnixMilli()}
		if d.Runtime > d.Position { // otherwise (bad metadata / live) show elapsed time only
			ts.End = start.Add(d.Runtime).UnixMilli()
		}
		a.Timestamps = ts
	}
	if len(errs) > 0 {
		return a, fmt.Errorf("template error: %w", errs[0])
	}
	return a, nil
}

// imageURL picks the best Jellyfin artwork for an item.
func (r *Renderer) imageURL(it *jellyfin.Item) string {
	base := r.cfg.ImageBaseURL()
	if base == "" {
		return ""
	}
	h := r.cfg.Images.MaxHeight
	primary := it.ImageTags["Primary"]
	switch MediaKey(it.Type) {
	case config.MediaEpisode:
		switch r.cfg.Images.EpisodeSource {
		case "episode":
			if primary != "" {
				return jellyfin.ImageURL(base, it.ID, "Primary", primary, h)
			}
		case "season":
			if it.SeasonID != "" {
				return jellyfin.ImageURL(base, it.SeasonID, "Primary", "", h)
			}
		}
		if it.SeriesID != "" {
			return jellyfin.ImageURL(base, it.SeriesID, "Primary", it.SeriesPrimaryImageTag, h)
		}
	case config.MediaAudio:
		if it.AlbumID != "" && it.AlbumPrimaryImageTag != "" {
			return jellyfin.ImageURL(base, it.AlbumID, "Primary", it.AlbumPrimaryImageTag, h)
		}
		if primary != "" {
			return jellyfin.ImageURL(base, it.ID, "Primary", primary, h)
		}
		if len(it.AlbumArtists) > 0 && it.AlbumArtists[0].ID != "" {
			return jellyfin.ImageURL(base, it.AlbumArtists[0].ID, "Primary", "", h)
		}
		return ""
	}
	if primary != "" {
		return jellyfin.ImageURL(base, it.ID, "Primary", primary, h)
	}
	return ""
}
