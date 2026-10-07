package presence

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/discord"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/jellyfin"
)

func loadSession(t *testing.T, name string) *jellyfin.Session {
	t.Helper()
	b, err := os.ReadFile("../jellyfin/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var ss []jellyfin.Session
	if err := json.Unmarshal(b, &ss); err != nil {
		t.Fatal(err)
	}
	for i := range ss {
		if ss[i].NowPlayingItem != nil {
			return &ss[i]
		}
	}
	t.Fatal("no playing session in fixture")
	return nil
}

func testConfig() *config.Config {
	c := config.Default()
	c.Jellyfin.URL = "http://10.0.0.2:8096"
	c.Jellyfin.PublicURL = "https://jf.example.com"
	return c
}

func TestRenderEpisode(t *testing.T) {
	cfg := testConfig()
	r, err := NewRenderer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := loadSession(t, "sessions_episode_12.2.json")
	now := time.UnixMilli(1_000_000_000)
	a, err := r.Render(s, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != discord.ActivityWatching || a.StatusDisplayType != discord.StatusDisplayDetails {
		t.Fatalf("type/status: %d/%d", a.Type, a.StatusDisplayType)
	}
	if a.Details != "The Rookie" {
		t.Errorf("details = %q", a.Details)
	}
	if a.State != "S01E05 · The Roundup" {
		t.Errorf("state = %q", a.State)
	}
	if a.Timestamps == nil || a.Timestamps.Start != now.Add(-160*time.Second).UnixMilli() ||
		a.Timestamps.End-a.Timestamps.Start != (10*time.Minute).Milliseconds() {
		t.Errorf("timestamps = %+v", a.Timestamps)
	}
	wantImg := "https://jf.example.com/Items/" + s.NowPlayingItem.SeriesID + "/Images/Primary?"
	if a.Assets == nil || !strings.HasPrefix(a.Assets.LargeImage, wantImg) {
		t.Errorf("large image = %+v", a.Assets)
	}
	if len(a.Buttons) != 1 || a.Buttons[0].URL != "https://www.imdb.com/title/tt8815074/" {
		t.Errorf("buttons = %+v", a.Buttons)
	}
}

func TestRenderMoviePausedAndFallbackImage(t *testing.T) {
	cfg := testConfig()
	cfg.Jellyfin.PublicURL = ""
	cfg.Behavior.Paused = config.PausedShow
	r, _ := NewRenderer(cfg)
	s := loadSession(t, "sessions_movie_paused_12.2.json")
	a, err := r.Render(s, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a.Details != "Inception" || !strings.HasPrefix(a.State, "2010 · ") {
		t.Errorf("details/state = %q / %q", a.Details, a.State)
	}
	if a.Timestamps != nil {
		t.Error("paused activity should not have timestamps")
	}
	if a.Assets.LargeImage != "jellyfin" {
		t.Errorf("expected fallback asset, got %q", a.Assets.LargeImage)
	}
}

func TestRenderAudio(t *testing.T) {
	cfg := testConfig()
	r, _ := NewRenderer(cfg)
	s := &jellyfin.Session{
		NowPlayingItem: &jellyfin.Item{
			ID: "song", Type: "Audio", Name: "Bohemian Rhapsody", Album: "A Night at the Opera",
			AlbumID: "alb", AlbumPrimaryImageTag: "t", Artists: []string{"Queen"}, RunTimeTicks: 354 * jellyfin.TicksPerSecond,
		},
	}
	a, _ := r.Render(s, time.Now())
	if a.Type != discord.ActivityListening || a.StatusDisplayType != discord.StatusDisplayState {
		t.Fatalf("type/status %d/%d", a.Type, a.StatusDisplayType)
	}
	if a.Details != "Bohemian Rhapsody" || a.State != "Queen" || a.Assets.LargeText != "A Night at the Opera" {
		t.Errorf("got %+v %+v", a, a.Assets)
	}
	if !strings.Contains(a.Assets.LargeImage, "/Items/alb/Images/Primary") {
		t.Errorf("image %q", a.Assets.LargeImage)
	}
}

func TestCustomTemplatesAndLimits(t *testing.T) {
	cfg := testConfig()
	cfg.Templates.Episode.Details = `{{upper .SeriesName}} {{percent .Position .Runtime}}%`
	cfg.Templates.Episode.State = `{{.Name}}` + strings.Repeat("x", 200)
	cfg.Templates.Episode.Buttons = []config.Button{{Label: "Watch", URL: "{{.WebURL}}"}, {Label: "none", URL: ""}}
	r, err := NewRenderer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := r.Render(loadSession(t, "sessions_episode_12.2.json"), time.Now())
	if a.Details != "THE ROOKIE 27%" {
		t.Errorf("details %q", a.Details)
	}
	if n := len([]rune(a.State)); n != 128 || !strings.HasSuffix(a.State, "…") {
		t.Errorf("state not truncated: %d", n)
	}
	if len(a.Buttons) != 1 || !strings.HasPrefix(a.Buttons[0].URL, "https://jf.example.com/web/#/details?id=") {
		t.Errorf("buttons %+v", a.Buttons)
	}
}

func TestBadTemplate(t *testing.T) {
	cfg := testConfig()
	cfg.Templates.Movie.State = "{{.Nope"
	if _, err := NewRenderer(cfg); err == nil || !strings.Contains(err.Error(), "templates.movie.state") {
		t.Fatalf("expected named template error, got %v", err)
	}
}

func TestEpcodeAndShortText(t *testing.T) {
	cases := []struct {
		d    Data
		want string
	}{
		{Data{HasSeason: true, Season: 0, HasEpisode: true, Episode: 3}, "S00E03"},
		{Data{HasSeason: true, Season: 2, HasEpisode: true, Episode: 1, EpisodeEnd: 2}, "S02E01-E02"},
		{Data{HasEpisode: true, Episode: 12}, "E12"},
		{Data{}, ""},
	}
	for _, c := range cases {
		if got := epcode(c.d); got != c.want {
			t.Errorf("epcode(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
	if got := fitText("A"); len([]rune(got)) != 2 {
		t.Errorf("short text not padded: %q", got)
	}
	if formatDuration(3723*time.Second) != "1:02:03" || formatDuration(65*time.Second) != "1:05" {
		t.Error("formatDuration")
	}
}
