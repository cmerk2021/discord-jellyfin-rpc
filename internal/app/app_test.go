package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/discord"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/jellyfin"
)

type fakePresence struct {
	mu        sync.Mutex
	up        bool // whether "Discord" is running
	connected bool
	sets      []*discord.Activity
	connects  int
}

func (f *fakePresence) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	if !f.up {
		return discord.ErrNotRunning
	}
	f.connected = true
	return nil
}
func (f *fakePresence) Connected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }
func (f *fakePresence) User() string    { return "tester" }
func (f *fakePresence) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
	return nil
}
func (f *fakePresence) SetActivity(a *discord.Activity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.up {
		f.connected = false
		return errors.New("broken pipe")
	}
	f.sets = append(f.sets, a)
	return nil
}
func (f *fakePresence) last() (*discord.Activity, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sets) == 0 {
		return nil, 0
	}
	return f.sets[len(f.sets)-1], len(f.sets)
}

func fixtureSessions(t *testing.T) []jellyfin.Session {
	b, err := os.ReadFile("../jellyfin/testdata/sessions_episode_12.2.json")
	if err != nil {
		t.Fatal(err)
	}
	var ss []jellyfin.Session
	if err := json.Unmarshal(b, &ss); err != nil {
		t.Fatal(err)
	}
	return ss
}

func newTestApp(t *testing.T, handler http.HandlerFunc) (*App, *fakePresence) {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Jellyfin.URL = srv.URL
	cfg.Jellyfin.Token = "tok"
	cfg.Jellyfin.UserID = "069cb963-5139-48c7-b69a-b55fe29ce1a5" // dashed form must still match
	cfg.Discord.ClientID = "1"
	cfg.Behavior.MinUpdateInterval = config.D(0)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakePresence{up: true}
	a.dc = fp
	return a, fp
}

func TestTickShowsAndClears(t *testing.T) {
	var playing atomic.Bool
	playing.Store(true)
	ss := fixtureSessions(t)
	a, fp := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		out := ss
		if !playing.Load() {
			out = []jellyfin.Session{}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	ctx := context.Background()
	if wait := a.tick(ctx); wait != a.cfg.Behavior.PollInterval.Duration {
		t.Fatalf("playing wait = %v", wait)
	}
	act, n := fp.last()
	if n != 1 || act == nil || act.Details != "The Rookie" {
		t.Fatalf("expected presence, got %+v (%d)", act, n)
	}
	// Same state again: no update sent.
	a.tick(ctx)
	if _, n := fp.last(); n != 1 {
		t.Fatalf("expected dedupe, got %d sets", n)
	}
	playing.Store(false)
	if wait := a.tick(ctx); wait != a.cfg.Behavior.IdlePollInterval.Duration {
		t.Fatalf("idle wait = %v", wait)
	}
	act, n = fp.last()
	if n != 2 || act != nil {
		t.Fatalf("expected clear, got %+v (%d)", act, n)
	}
}

func TestJellyfinDownClearsAndBacksOff(t *testing.T) {
	var down atomic.Bool
	ss := fixtureSessions(t)
	a, fp := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(ss)
	})
	ctx := context.Background()
	a.tick(ctx)
	down.Store(true)
	w1 := a.tick(ctx)
	if act, _ := fp.last(); act != nil {
		t.Fatal("presence should be cleared while jellyfin is down")
	}
	a.tick(ctx)
	w3 := a.tick(ctx)
	if w3 <= w1 {
		t.Fatalf("expected growing backoff: %v then %v", w1, w3)
	}
	down.Store(false)
	a.tick(ctx)
	if act, _ := fp.last(); act == nil {
		t.Fatal("presence should be restored")
	}
}

func TestDiscordRestartRecovers(t *testing.T) {
	ss := fixtureSessions(t)
	a, fp := newTestApp(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(ss) })
	ctx := context.Background()
	fp.up = false
	a.tick(ctx)
	if fp.Connected() {
		t.Fatal("should not be connected")
	}
	a.tick(ctx) // within backoff window: no extra connect attempt
	if fp.connects != 1 {
		t.Fatalf("connect attempts = %d, want 1 (backoff)", fp.connects)
	}
	fp.up = true
	a.nextDCTry = time.Time{}
	a.tick(ctx)
	if act, _ := fp.last(); act == nil || !fp.Connected() {
		t.Fatal("should reconnect and set presence")
	}
	// Discord quits mid-session, then comes back.
	fp.mu.Lock()
	fp.up = false
	fp.mu.Unlock()
	a.current = nil // force a write
	a.tick(ctx)
	if fp.Connected() {
		t.Fatal("should detect disconnect")
	}
	fp.mu.Lock()
	fp.up = true
	fp.mu.Unlock()
	a.nextDCTry = time.Time{}
	a.tick(ctx)
	if !fp.Connected() {
		t.Fatal("should reconnect")
	}
}

func TestUserFilter(t *testing.T) {
	ss := fixtureSessions(t)
	a, fp := newTestApp(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(ss) })
	a.userID = "someone-else"
	a.tick(context.Background())
	if _, n := fp.last(); n != 0 {
		t.Fatal("other users' sessions must not be shown")
	}
}

func TestPanicRecovered(t *testing.T) {
	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode([]any{}) })
	a.dc = nil // will panic in sync
	if wait := a.tick(context.Background()); wait != a.cfg.Behavior.MaxBackoff.Duration {
		t.Fatalf("wait after panic = %v", wait)
	}
}

func TestSameActivity(t *testing.T) {
	x := &discord.Activity{Details: "a", Timestamps: &discord.Timestamps{Start: 10_000, End: 20_000}}
	y := &discord.Activity{Details: "a", Timestamps: &discord.Timestamps{Start: 14_000, End: 24_000}}
	if !sameActivity(x, y, 5*time.Second) {
		t.Error("small drift should be equal")
	}
	if sameActivity(x, y, 2*time.Second) {
		t.Error("large drift should differ")
	}
	if sameActivity(x, &discord.Activity{Details: "b", Timestamps: x.Timestamps}, time.Hour) {
		t.Error("different details should differ")
	}
	if !sameActivity(nil, nil, 0) || sameActivity(x, nil, 0) {
		t.Error("nil handling")
	}
}

func TestBackoff(t *testing.T) {
	base, max := 5*time.Second, 60*time.Second
	prev := time.Duration(0)
	for i := 1; i <= 10; i++ {
		d := backoff(base, max, i)
		if d > max+max/10 || d < base-base/10 {
			t.Fatalf("backoff(%d) = %v out of range", i, d)
		}
		if i <= 3 && d < prev {
			t.Fatalf("backoff should grow: %v < %v", d, prev)
		}
		prev = d
	}
}

func TestPickPrefersPlaying(t *testing.T) {
	now := time.Now()
	ss := []jellyfin.Session{
		{ID: "paused", PlayState: jellyfin.PlayState{IsPaused: true}, LastActivityDate: jellyfin.Time{Time: now}},
		{ID: "old", LastActivityDate: jellyfin.Time{Time: now.Add(-time.Hour)}},
		{ID: "new", LastActivityDate: jellyfin.Time{Time: now.Add(-time.Minute)}},
	}
	if p := pick(ss); p.ID != "new" {
		t.Fatalf("picked %s", p.ID)
	}
}

func TestReferenceTime(t *testing.T) {
	now := time.Now()
	s := &jellyfin.Session{LastPlaybackCheckIn: jellyfin.Time{Time: now.Add(-5 * time.Second)}}
	if !referenceTime(s, now).Equal(now.Add(-5 * time.Second)) {
		t.Error("should use check-in time")
	}
	s.LastPlaybackCheckIn.Time = now.Add(time.Hour)
	if !referenceTime(s, now).Equal(now) {
		t.Error("should fall back to now on skew")
	}
}
