package app

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/jellyfin"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/presence"
)

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(v string) bool { return strings.EqualFold(v, s) })
}

// matchesUser reports whether the session belongs to the configured user.
func matchesUser(cfg *config.Config, userID string, s *jellyfin.Session) bool {
	if userID != "" {
		return strings.EqualFold(strings.ReplaceAll(s.UserID, "-", ""), strings.ReplaceAll(userID, "-", ""))
	}
	if cfg.Jellyfin.Username != "" {
		return strings.EqualFold(s.UserName, cfg.Jellyfin.Username)
	}
	return true
}

// eligible applies the user, client, device and media type filters (not library filters).
func eligible(cfg *config.Config, userID string, s *jellyfin.Session) bool {
	if s.NowPlayingItem == nil || s.NowPlayingItem.ID == "" {
		return false
	}
	if !matchesUser(cfg, userID, s) {
		return false
	}
	j := cfg.Jellyfin
	if len(j.Clients) > 0 && !containsFold(j.Clients, s.Client) {
		return false
	}
	if len(j.Devices) > 0 && !containsFold(j.Devices, s.DeviceName) {
		return false
	}
	if containsFold(j.IgnoreClients, s.Client) {
		return false
	}
	if !cfg.MediaEnabled(presence.MediaKey(s.NowPlayingItem.Type)) {
		return false
	}
	if s.PlayState.IsPaused && cfg.Behavior.Paused == config.PausedClear {
		return false
	}
	return true
}

// libraryFilter caches item -> library name lookups so ignore_libraries costs one request per item.
type libraryFilter struct {
	mu    sync.Mutex
	cache map[string]bool // itemID -> ignored
}

func (f *libraryFilter) ignored(ctx context.Context, jf *jellyfin.Client, cfg *config.Config, userID, itemID string) (bool, error) {
	if len(cfg.Jellyfin.IgnoreLibraries) == 0 {
		return false, nil
	}
	f.mu.Lock()
	if v, ok := f.cache[itemID]; ok {
		f.mu.Unlock()
		return v, nil
	}
	f.mu.Unlock()
	anc, err := jf.Ancestors(ctx, itemID, userID)
	if err != nil {
		return false, err
	}
	ign := false
	for _, a := range anc {
		if containsFold(cfg.Jellyfin.IgnoreLibraries, a.Name) {
			ign = true
			break
		}
	}
	f.mu.Lock()
	if f.cache == nil || len(f.cache) > 256 {
		f.cache = map[string]bool{}
	}
	f.cache[itemID] = ign
	f.mu.Unlock()
	return ign, nil
}

// pick chooses the session to display: playing beats paused, then most recent activity.
func pick(sessions []jellyfin.Session) *jellyfin.Session {
	var best *jellyfin.Session
	for i := range sessions {
		s := &sessions[i]
		if best == nil {
			best = s
			continue
		}
		if best.PlayState.IsPaused != s.PlayState.IsPaused {
			if !s.PlayState.IsPaused {
				best = s
			}
			continue
		}
		if s.LastActivityDate.After(best.LastActivityDate.Time) {
			best = s
		}
	}
	return best
}
