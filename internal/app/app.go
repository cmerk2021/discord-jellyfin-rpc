// Package app runs the Jellyfin -> Discord presence loop.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/user"
	"reflect"
	"runtime/debug"
	"time"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/discord"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/jellyfin"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/presence"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/version"
)

// Presence is the subset of the Discord client used by the app.
type Presence interface {
	Connect() error
	Connected() bool
	SetActivity(*discord.Activity) error
	Close() error
	User() string
}

// App holds runtime state.
type App struct {
	log      *slog.Logger
	cfg      *config.Config
	jf       *jellyfin.Client
	renderer *presence.Renderer
	dc       Presence
	libs     libraryFilter

	// NewPresence creates the Discord client; overridable in tests.
	NewPresence func(clientID string) Presence

	userID     string
	current    *discord.Activity
	lastSent   time.Time
	jfFails    int
	dcFails    int
	nextDCTry  time.Time
	jfState    string
	dcState    string
	lastRPCErr string
}

// DeviceID returns the configured device ID or a stable one derived from host and user.
func DeviceID(cfg *config.Config) string {
	if cfg.Jellyfin.DeviceID != "" {
		return cfg.Jellyfin.DeviceID
	}
	host, _ := os.Hostname()
	u := ""
	if cu, err := user.Current(); err == nil {
		u = cu.Username
	}
	sum := sha256.Sum256([]byte("jellyfin-rpc|" + host + "|" + u))
	return hex.EncodeToString(sum[:16])
}

// NewJellyfin builds a Jellyfin client from config.
func NewJellyfin(cfg *config.Config) *jellyfin.Client {
	token := cfg.Jellyfin.Token
	if cfg.Jellyfin.AuthMethod == config.AuthAPIKey {
		token = cfg.Jellyfin.APIKey
	}
	host, _ := os.Hostname()
	return jellyfin.New(jellyfin.Options{
		BaseURL:            cfg.Jellyfin.URL,
		Token:              token,
		DeviceID:           DeviceID(cfg),
		DeviceName:         "jellyfin-rpc (" + host + ")",
		Version:            version.Version,
		Timeout:            cfg.Jellyfin.Timeout.Duration,
		InsecureSkipVerify: cfg.Jellyfin.InsecureSkipVerify,
	})
}

// New creates an App.
func New(cfg *config.Config, log *slog.Logger) (*App, error) {
	r, err := presence.NewRenderer(cfg)
	if err != nil {
		return nil, err
	}
	a := &App{
		log:         log,
		cfg:         cfg,
		jf:          NewJellyfin(cfg),
		renderer:    r,
		userID:      cfg.Jellyfin.UserID,
		NewPresence: func(id string) Presence { return discord.New(id) },
	}
	return a, nil
}

// Run polls until ctx is cancelled. A value on reload triggers a config reload from load().
func (a *App) Run(ctx context.Context, reload <-chan struct{}, load func() (*config.Config, error)) error {
	if a.dc == nil {
		a.dc = a.NewPresence(a.cfg.Discord.ClientID)
	}
	a.log.Info("starting", "version", version.Version, "server", a.cfg.Jellyfin.URL)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			a.shutdown()
			return nil
		case <-reload:
			if load != nil {
				a.reload(load)
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)
		case <-timer.C:
			timer.Reset(a.tick(ctx))
		}
	}
}

func (a *App) reload(load func() (*config.Config, error)) {
	cfg, err := load()
	if err != nil {
		a.log.Error("config reload failed; keeping previous config", "err", err)
		return
	}
	r, err := presence.NewRenderer(cfg)
	if err != nil {
		a.log.Error("config reload failed; keeping previous config", "err", err)
		return
	}
	if cfg.Discord.ClientID != a.cfg.Discord.ClientID {
		a.clear()
		_ = a.dc.Close()
		a.dc = a.NewPresence(cfg.Discord.ClientID)
		a.current = nil
	}
	a.jf.CloseIdle()
	a.cfg, a.renderer, a.jf = cfg, r, NewJellyfin(cfg)
	a.userID = cfg.Jellyfin.UserID
	a.libs = libraryFilter{}
	a.jfFails, a.dcFails, a.nextDCTry = 0, 0, time.Time{}
	a.log.Info("config reloaded")
}

// backoff returns an exponential backoff with jitter, capped at max.
func backoff(base, max time.Duration, fails int) time.Duration {
	if fails <= 0 {
		return base
	}
	d := base
	for i := 1; i < fails && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	jitter := time.Duration(rand.Int64N(int64(d)/5 + 1))
	return d - d/10 + jitter
}

// tick performs one poll+sync cycle and returns the delay until the next one.
func (a *App) tick(ctx context.Context) (wait time.Duration) {
	b := a.cfg.Behavior
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("recovered from panic", "panic", r, "stack", string(debug.Stack()))
			wait = b.MaxBackoff.Duration
		}
	}()
	pctx, cancel := context.WithTimeout(ctx, a.cfg.Jellyfin.Timeout.Duration+5*time.Second)
	defer cancel()
	desired, err := a.poll(pctx)
	if ctx.Err() != nil {
		return 0
	}
	if err != nil {
		a.jfFails++
		state := "unreachable"
		if jellyfin.IsUnauthorized(err) {
			state = "unauthorized"
		}
		if a.jfState != state {
			if state == "unauthorized" {
				a.log.Error("jellyfin rejected credentials; re-run `jellyfin-rpc setup`", "err", err)
			} else {
				a.log.Warn("jellyfin unavailable; retrying with backoff", "err", err)
			}
			a.jfState = state
		} else {
			a.log.Debug("jellyfin still unavailable", "err", err, "attempt", a.jfFails)
		}
		a.sync(nil)
		if state == "unauthorized" {
			return b.MaxBackoff.Duration
		}
		return backoff(b.PollInterval.Duration, b.MaxBackoff.Duration, a.jfFails)
	}
	if a.jfState != "ok" {
		if a.jfState != "" {
			a.log.Info("jellyfin reachable again")
		}
		a.jfState = "ok"
	}
	a.jfFails = 0
	a.sync(desired)
	if desired != nil {
		return b.PollInterval.Duration
	}
	return b.IdlePollInterval.Duration
}

// poll fetches sessions and renders the desired activity (nil = nothing to show).
func (a *App) poll(ctx context.Context) (*discord.Activity, error) {
	if a.userID == "" && a.cfg.Jellyfin.AuthMethod == config.AuthPassword {
		me, err := a.jf.Me(ctx)
		if err != nil {
			return nil, err
		}
		a.userID = me.ID
	}
	sessions, err := a.jf.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []jellyfin.Session
	for i := range sessions {
		s := &sessions[i]
		if !eligible(a.cfg, a.userID, s) {
			continue
		}
		ign, err := a.libs.ignored(ctx, a.jf, a.cfg, a.userID, s.NowPlayingItem.ID)
		if err != nil {
			a.log.Debug("library lookup failed", "item", s.NowPlayingItem.ID, "err", err)
		}
		if ign {
			continue
		}
		candidates = append(candidates, *s)
	}
	s := pick(candidates)
	if s == nil {
		return nil, nil
	}
	act, err := a.renderer.Render(s, referenceTime(s, time.Now()))
	if err != nil {
		a.log.Warn("template rendering problem", "err", err)
	}
	return act, nil
}

// referenceTime returns when PositionTicks was reported, so the progress bar doesn't jitter
// between client progress reports. Falls back to now if the server clock looks skewed.
func referenceTime(s *jellyfin.Session, now time.Time) time.Time {
	t := s.LastPlaybackCheckIn.Time
	if t.IsZero() || t.Year() < 2000 {
		return now
	}
	if d := now.Sub(t); d < -time.Minute || d > 2*time.Minute {
		return now
	}
	return t
}

// sameActivity compares activities, tolerating small timestamp drift.
func sameActivity(x, y *discord.Activity, tolerance time.Duration) bool {
	if x == nil || y == nil {
		return x == y
	}
	xc, yc := *x, *y
	xt, yt := xc.Timestamps, yc.Timestamps
	xc.Timestamps, yc.Timestamps = nil, nil
	if !reflect.DeepEqual(xc, yc) {
		return false
	}
	if (xt == nil) != (yt == nil) {
		return false
	}
	if xt == nil {
		return true
	}
	tol := tolerance.Milliseconds()
	abs := func(v int64) int64 {
		if v < 0 {
			return -v
		}
		return v
	}
	if (xt.End == 0) != (yt.End == 0) {
		return false
	}
	return abs(xt.Start-yt.Start) <= tol && abs(xt.End-yt.End) <= tol
}

// sync pushes desired to Discord if it differs from what is shown.
func (a *App) sync(desired *discord.Activity) {
	connected := a.dc.Connected()
	if desired == nil && !connected {
		return // nothing to show and nothing shown; don't bother connecting
	}
	if connected && sameActivity(desired, a.current, a.cfg.Behavior.SeekThreshold.Duration) {
		return
	}
	now := time.Now()
	if connected && now.Sub(a.lastSent) < a.cfg.Behavior.MinUpdateInterval.Duration {
		return // rate limit; retried next tick
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !a.dc.Connected() {
			if !a.connect(now) {
				return
			}
			if desired == nil {
				return // fresh connection has no activity
			}
		}
		err := a.dc.SetActivity(desired)
		if err == nil {
			a.current, a.lastSent, a.lastRPCErr = desired, now, ""
			if desired != nil {
				a.log.Debug("presence updated", "details", desired.Details, "state", desired.State)
			} else {
				a.log.Debug("presence cleared")
			}
			return
		}
		var rpcErr *discord.RPCError
		if errors.As(err, &rpcErr) && a.dc.Connected() {
			// Discord rejected the payload; remember it to avoid retry spam.
			if msg := err.Error(); msg != a.lastRPCErr {
				a.log.Warn("discord rejected activity", "err", err)
				a.lastRPCErr = msg
			}
			a.current, a.lastSent = desired, now
			return
		}
		a.log.Warn("lost connection to discord", "err", err)
		a.dcState = "down"
		a.current = nil
	}
}

func (a *App) connect(now time.Time) bool {
	if now.Before(a.nextDCTry) {
		return false
	}
	if err := a.dc.Connect(); err != nil {
		a.dcFails++
		a.nextDCTry = now.Add(backoff(2*time.Second, a.cfg.Behavior.MaxBackoff.Duration, a.dcFails))
		if a.dcState != "down" {
			a.log.Warn("discord not available; will keep retrying", "err", err)
			a.dcState = "down"
		} else {
			a.log.Debug("discord still unavailable", "err", err, "attempt", a.dcFails)
		}
		return false
	}
	a.dcFails, a.nextDCTry, a.current = 0, time.Time{}, nil
	a.dcState = "ok"
	a.log.Info("connected to discord", "user", a.dc.User())
	return true
}

func (a *App) clear() {
	if a.dc != nil && a.dc.Connected() && a.current != nil {
		if err := a.dc.SetActivity(nil); err != nil {
			a.log.Debug("clear presence failed", "err", err)
		}
		a.current = nil
	}
}

func (a *App) shutdown() {
	a.log.Info("shutting down")
	a.clear()
	if a.dc != nil {
		_ = a.dc.Close()
	}
	a.jf.CloseIdle()
}

// Preview renders the activity for the current session once (used by `check`).
func (a *App) Preview(ctx context.Context) (*discord.Activity, error) {
	act, err := a.poll(ctx)
	if err != nil {
		return nil, fmt.Errorf("query jellyfin: %w", err)
	}
	return act, nil
}
