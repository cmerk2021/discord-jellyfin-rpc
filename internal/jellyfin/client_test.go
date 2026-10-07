package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// legacyCheck fails the test if a request uses anything removed in Jellyfin 12.
func legacyCheck(t *testing.T, r *http.Request) {
	t.Helper()
	for _, h := range []string{"X-Emby-Token", "X-MediaBrowser-Token", "X-Emby-Authorization"} {
		if r.Header.Get(h) != "" {
			t.Errorf("request uses legacy header %s", h)
		}
	}
	if r.URL.Query().Has("api_key") || r.URL.Query().Has("ApiKey") {
		t.Errorf("request passes token in query string: %s", r.URL)
	}
	p := strings.ToLower(r.URL.Path)
	if strings.HasPrefix(p, "/emby/") || strings.HasPrefix(p, "/mediabrowser/") {
		t.Errorf("request uses legacy route prefix: %s", r.URL.Path)
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "MediaBrowser ") {
		t.Errorf("missing MediaBrowser Authorization header")
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAuthorizationHeader(t *testing.T) {
	c := New(Options{BaseURL: "http://x", Token: "abc123", DeviceID: "dev", DeviceName: `my "box"`, Version: "1.2.3"})
	got := c.AuthorizationHeader()
	want := `MediaBrowser Client="jellyfin-rpc", Device="my%20box", DeviceId="dev", Version="1.2.3", Token="abc123"`
	if got != want {
		t.Fatalf("header\n got %s\nwant %s", got, want)
	}
	c.SetToken("")
	if strings.Contains(c.AuthorizationHeader(), "Token=") {
		t.Fatal("token should be omitted when empty")
	}
}

func TestSessionsDecodesJellyfin12(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCheck(t, r)
		if r.URL.Path != "/Sessions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Authorization"), `Token="tok"`) {
			t.Errorf("token missing from Authorization header")
		}
		w.Write(fixture(t, "sessions_episode_12.2.json"))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL + "/", Token: "tok", DeviceID: "d"})
	ss, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var s *Session
	for i := range ss {
		if ss[i].NowPlayingItem != nil {
			s = &ss[i]
		}
	}
	if s == nil {
		t.Fatal("no playing session decoded")
	}
	it := s.NowPlayingItem
	if it.Type != "Episode" || it.SeriesName != "The Rookie" || it.Name != "The Roundup" {
		t.Fatalf("bad item: %+v", it)
	}
	if *it.ParentIndexNumber != 1 || *it.IndexNumber != 5 {
		t.Fatalf("bad numbers: %v %v", *it.ParentIndexNumber, *it.IndexNumber)
	}
	if it.SeriesID == "" || it.SeriesPrimaryImageTag == "" || it.ProviderIDs["Imdb"] == "" {
		t.Fatalf("missing ids: %+v", it)
	}
	if s.PlayState.PositionTicks != 1_600_000_000 || TicksToDuration(it.RunTimeTicks) != 10*time.Minute {
		t.Fatalf("bad play state %+v runtime %d", s.PlayState, it.RunTimeTicks)
	}
	if s.LastPlaybackCheckIn.IsZero() || s.LastActivityDate.IsZero() {
		t.Fatal("timestamps not decoded")
	}
}

func TestPublicInfoCamelCase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCheck(t, r)
		w.Write(fixture(t, "public_info_12.2.json"))
	}))
	defer srv.Close()
	info, err := New(Options{BaseURL: srv.URL}).PublicInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "12.2.0" || info.ServerName == "" {
		t.Fatalf("bad info %+v", info)
	}
}

func TestAuthenticateByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCheck(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/Users/AuthenticateByName" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if strings.Contains(r.Header.Get("Authorization"), "Token=") {
			t.Error("login should not send a token")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["Username"] != "bob" || body["Pw"] != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"User":{"Id":"u1","Name":"bob"},"AccessToken":"newtok"}`))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, DeviceID: "d"})
	res, err := c.AuthenticateByName(context.Background(), "bob", "pw")
	if err != nil || res.AccessToken != "newtok" || res.User.ID != "u1" {
		t.Fatalf("login: %+v %v", res, err)
	}
	_, err = c.AuthenticateByName(context.Background(), "bob", "wrong")
	if !IsUnauthorized(err) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestLenientTime(t *testing.T) {
	var v struct{ T Time }
	for _, in := range []string{`{"T":"2026-10-07T22:43:49.4522019Z"}`, `{"T":"garbage"}`, `{"T":null}`, `{}`} {
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
	}
}

func TestImageURL(t *testing.T) {
	got := ImageURL("https://jf.example.com/", "abc", "Primary", "tag1", 512)
	want := "https://jf.example.com/Items/abc/Images/Primary?maxHeight=512&quality=90&tag=tag1"
	if got != want {
		t.Fatalf("got %s", got)
	}
	if ImageURL("", "abc", "Primary", "", 0) != "" {
		t.Fatal("expected empty URL without base")
	}
}
