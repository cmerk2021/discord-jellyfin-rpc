// Package setup implements the interactive configuration wizard.
package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/app"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/discord"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/jellyfin"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/service"
)

// Options controls the wizard.
type Options struct {
	ConfigPath string
	// ServicePrompt asks whether to install the background service at the end.
	ServicePrompt bool
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func normalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not a valid http(s) URL", s)
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimSuffix(u.Path, "/web/")
	u.Path = strings.TrimSuffix(u.Path, "/web")
	return strings.TrimRight(u.String(), "/"), nil
}

// versionAtLeast compares dotted versions numerically.
func versionAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return true // unknown format, don't nag
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	return ma > major || (ma == major && mi >= minor)
}

// Run executes the wizard.
func Run(ctx context.Context, opts Options) error {
	p, closeFn := NewPrompter()
	defer closeFn()
	return run(ctx, p, opts)
}

func run(ctx context.Context, p *Prompter, opts Options) error {
	cfg, err := config.Load(opts.ConfigPath)
	existing := err == nil
	if err != nil {
		cfg = config.Default()
		if !errors.Is(err, config.ErrNotFound) {
			// Keep whatever parsed so the user doesn't lose settings on a validation error.
			p.Warn("Existing config has problems; values will be re-asked: " + err.Error())
			_ = config.DecodeLoose(opts.ConfigPath, cfg)
		}
	}
	if cfg.Jellyfin.DeviceID == "" {
		cfg.Jellyfin.DeviceID = randomID()
	}

	p.Printf("\n%s\n", p.color(bold, "jellyfin-rpc setup"))
	p.Info("Shows what you're watching on Jellyfin as your Discord status. Press Enter to accept [defaults].")
	if existing {
		p.Info("Editing existing config: " + opts.ConfigPath)
	}

	// --- Server ---
	p.Header("Jellyfin server")
	var jf *jellyfin.Client
	for {
		raw, err := p.AskRequired("Server URL (e.g. http://192.168.1.10:8096)", cfg.Jellyfin.URL)
		if err != nil {
			return err
		}
		u, err := normalizeURL(raw)
		if err != nil {
			p.Err(err.Error())
			continue
		}
		cfg.Jellyfin.URL = u
		jf = app.NewJellyfin(cfg)
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		info, err := jf.PublicInfo(c)
		cancel()
		if err != nil {
			p.Err("Could not reach Jellyfin: " + err.Error())
			if strings.HasPrefix(u, "https://") {
				if ok, _ := p.Confirm("Is it using a self-signed certificate (skip TLS verification)?", false); ok {
					cfg.Jellyfin.InsecureSkipVerify = true
					continue
				}
			}
			if ok, _ := p.Confirm("Use this URL anyway?", false); !ok {
				continue
			}
			break
		}
		p.OK(fmt.Sprintf("Connected to %q (Jellyfin %s)", info.ServerName, info.Version))
		if !versionAtLeast(info.Version, 10, 10) {
			p.Warn("Jellyfin " + info.Version + " is old; jellyfin-rpc targets 10.10+ and 12.x. Some features may not work.")
		}
		break
	}

	// --- Public URL for artwork ---
	p.Header("Cover art")
	p.Info("Discord downloads cover art itself, so it needs a URL reachable from the internet")
	p.Info("(e.g. https://jellyfin.example.com). Leave empty to use a generic Jellyfin logo instead.")
	defPublic := cfg.Jellyfin.PublicURL
	if !existing && defPublic == "" && !isPrivateURL(cfg.Jellyfin.URL) {
		defPublic = cfg.Jellyfin.URL
	}
	for {
		v, err := p.Ask("Public URL", defPublic)
		if err != nil {
			return err
		}
		if v == "" || v == "-" {
			cfg.Jellyfin.PublicURL = ""
			p.Info("Cover art disabled; using fallback image.")
			break
		}
		u, err := normalizeURL(v)
		if err != nil {
			p.Err(err.Error())
			continue
		}
		if isPrivateURL(u) {
			p.Warn("That looks like a private/LAN address; Discord won't be able to load images from it.")
			if ok, _ := p.Confirm("Use it anyway?", false); !ok {
				continue
			}
		}
		cfg.Jellyfin.PublicURL = u
		break
	}
	cfg.Images.Enabled = cfg.Jellyfin.PublicURL != ""

	// --- Auth ---
	p.Header("Authentication")
	defAuth := 0
	if cfg.Jellyfin.AuthMethod == config.AuthAPIKey {
		defAuth = 1
	}
	choice, err := p.Choose("How should jellyfin-rpc sign in?", []string{
		"Username & password (recommended; password is not stored, only a session token)",
		"API key (Dashboard → API Keys; requires picking which user to follow)",
	}, defAuth)
	if err != nil {
		return err
	}
	if choice == 0 {
		if err := authPassword(ctx, p, cfg, jf); err != nil {
			return err
		}
	} else {
		if err := authAPIKey(ctx, p, cfg, jf); err != nil {
			return err
		}
	}

	// --- What to show ---
	p.Header("What to show")
	labels := map[string]string{
		config.MediaEpisode:    "TV episodes",
		config.MediaMovie:      "Movies",
		config.MediaAudio:      "Music & audiobooks",
		config.MediaMusicVideo: "Music videos",
		config.MediaLiveTV:     "Live TV",
		config.MediaOther:      "Other videos (home videos, etc.)",
	}
	var types []string
	for _, m := range config.AllMediaTypes {
		ok, err := p.Confirm("Show "+labels[m]+"?", cfg.MediaEnabled(m))
		if err != nil {
			return err
		}
		if ok {
			types = append(types, m)
		}
	}
	cfg.Behavior.MediaTypes = types

	pausedDef := 0
	if cfg.Behavior.Paused == config.PausedShow {
		pausedDef = 1
	}
	pc, err := p.Choose("When playback is paused:", []string{"Clear the status", "Keep showing it (without progress bar)"}, pausedDef)
	if err != nil {
		return err
	}
	cfg.Behavior.Paused = []string{config.PausedClear, config.PausedShow}[pc]

	hasButtons := len(cfg.Templates.Movie.Buttons) > 0 || len(cfg.Templates.Episode.Buttons) > 0
	btn, err := p.Confirm("Show IMDb/TMDB link buttons (visible to others, not yourself)?", hasButtons)
	if err != nil {
		return err
	}
	def := config.DefaultTemplates()
	if btn {
		if len(cfg.Templates.Movie.Buttons) == 0 {
			cfg.Templates.Movie.Buttons = def.Movie.Buttons
		}
		if len(cfg.Templates.Episode.Buttons) == 0 {
			cfg.Templates.Episode.Buttons = def.Episode.Buttons
		}
	} else {
		cfg.Templates.Movie.Buttons, cfg.Templates.Episode.Buttons = nil, nil
	}
	p.Info("Status lines, buttons and more can be customized later in " + opts.ConfigPath)

	// --- Discord ---
	p.Header("Discord")
	if cfg.Discord.ClientID == "" {
		cfg.Discord.ClientID = discord.DefaultClientID
	}
	if cfg.Discord.ClientID == "" {
		p.Info("Create an application at https://discord.com/developers/applications (name it e.g. \"Jellyfin\")")
		p.Info("and paste its Application ID here.")
	}
	for {
		id, err := p.AskRequired("Discord Application ID", cfg.Discord.ClientID)
		if err != nil {
			return err
		}
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			p.Err("Application IDs are numeric.")
			continue
		}
		cfg.Discord.ClientID = id
		break
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("generated config is invalid: %w", err)
	}
	if err := cfg.Save(opts.ConfigPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	p.OK("Saved config to " + opts.ConfigPath)

	// --- Test presence ---
	if ok, _ := p.Confirm("Send a test status to Discord now?", true); ok {
		testDiscord(p, cfg)
	}

	// --- Service ---
	if opts.ServicePrompt {
		p.Header("Background service")
		if err := offerService(p, opts.ConfigPath); err != nil {
			p.Err(err.Error())
		}
	}
	p.Printf("\n%s\n", p.color(bold+green, "All set!"))
	return nil
}

func authPassword(ctx context.Context, p *Prompter, cfg *config.Config, jf *jellyfin.Client) error {
	if cfg.Jellyfin.AuthMethod == config.AuthPassword && cfg.Jellyfin.Token != "" {
		jf.SetToken(cfg.Jellyfin.Token)
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		me, err := jf.Me(c)
		cancel()
		if err == nil {
			p.OK("Already signed in as " + me.Name)
			if keep, _ := p.Confirm("Keep this login?", true); keep {
				cfg.Jellyfin.UserID, cfg.Jellyfin.Username = me.ID, me.Name
				return nil
			}
		}
		jf.SetToken("")
	}
	for {
		user, err := p.AskRequired("Username", cfg.Jellyfin.Username)
		if err != nil {
			return err
		}
		pw, err := p.Secret("Password", false)
		if err != nil {
			return err
		}
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := jf.AuthenticateByName(c, user, pw)
		cancel()
		if err != nil {
			if jellyfin.IsUnauthorized(err) {
				p.Err("Invalid username or password.")
			} else {
				p.Err("Login failed: " + err.Error())
			}
			if again, _ := p.Confirm("Try again?", true); again {
				continue
			}
			return errors.New("setup aborted: could not sign in")
		}
		cfg.Jellyfin.AuthMethod = config.AuthPassword
		cfg.Jellyfin.Token = res.AccessToken
		cfg.Jellyfin.UserID, cfg.Jellyfin.Username = res.User.ID, res.User.Name
		cfg.Jellyfin.APIKey = ""
		p.OK("Signed in as " + res.User.Name)
		return nil
	}
}

func authAPIKey(ctx context.Context, p *Prompter, cfg *config.Config, jf *jellyfin.Client) error {
	for {
		key, err := p.Secret("API key", cfg.Jellyfin.APIKey != "")
		if err != nil {
			return err
		}
		if key == "" {
			key = cfg.Jellyfin.APIKey
		}
		if key == "" {
			p.Warn("An API key is required.")
			continue
		}
		jf.SetToken(key)
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		users, err := jf.Users(c)
		cancel()
		if err != nil {
			p.Err("API key check failed: " + err.Error())
			if again, _ := p.Confirm("Try again?", true); again {
				continue
			}
			return errors.New("setup aborted: invalid API key")
		}
		cfg.Jellyfin.AuthMethod = config.AuthAPIKey
		cfg.Jellyfin.APIKey = key
		cfg.Jellyfin.Token = ""
		if len(users) == 0 {
			name, err := p.AskRequired("Username to follow", cfg.Jellyfin.Username)
			if err != nil {
				return err
			}
			cfg.Jellyfin.Username, cfg.Jellyfin.UserID = name, ""
			return nil
		}
		names := make([]string, len(users))
		def := 0
		for i, u := range users {
			names[i] = u.Name
			if u.ID == cfg.Jellyfin.UserID || strings.EqualFold(u.Name, cfg.Jellyfin.Username) {
				def = i
			}
		}
		i, err := p.Choose("Whose playback should be shown?", names, def)
		if err != nil {
			return err
		}
		cfg.Jellyfin.UserID, cfg.Jellyfin.Username = users[i].ID, users[i].Name
		p.OK("Following " + users[i].Name)
		return nil
	}
}

func testDiscord(p *Prompter, cfg *config.Config) {
	dc := discord.New(cfg.Discord.ClientID)
	if err := dc.Connect(); err != nil {
		p.Warn("Couldn't connect to Discord (" + err.Error() + "). Make sure the desktop app is running; jellyfin-rpc will keep retrying in the background.")
		return
	}
	defer dc.Close()
	now := time.Now()
	act := &discord.Activity{
		Type:              discord.ActivityWatching,
		StatusDisplayType: discord.StatusDisplayDetails,
		Details:           "jellyfin-rpc test",
		State:             "S01E01 · It works!",
		Timestamps:        &discord.Timestamps{Start: now.UnixMilli(), End: now.Add(10 * time.Minute).UnixMilli()},
	}
	if cfg.Images.Fallback != "" {
		act.Assets = &discord.Assets{LargeImage: cfg.Images.Fallback, LargeText: "Jellyfin"}
	}
	if err := dc.SetActivity(act); err != nil {
		p.Warn("Discord rejected the test status: " + err.Error())
		return
	}
	p.OK("Connected to Discord as " + dc.User() + ". Check your profile for \"Watching jellyfin-rpc test\".")
	p.Info("Note: Discord must have \"Share my activity\" enabled (Settings → Activity Privacy).")
	_, _ = p.Ask("Press Enter to clear the test status", "")
	_ = dc.SetActivity(nil)
}

func offerService(p *Prompter, cfgPath string) error {
	mgr, err := service.New()
	if err != nil {
		p.Warn(err.Error())
		p.Info("Run `jellyfin-rpc run` from your desktop session's autostart instead.")
		return nil
	}
	msg := "Run jellyfin-rpc in the background and start it automatically when you log in?"
	if mgr.Installed() {
		msg = "Background service already installed. Reinstall/restart it with the new config?"
	}
	ok, err := p.Confirm(msg, true)
	if err != nil || !ok {
		if !ok {
			p.Info("Skipped. Start it manually with `jellyfin-rpc run`, or later with `jellyfin-rpc service install`.")
		}
		return err
	}
	exe, err := service.Executable()
	if err != nil {
		return err
	}
	o := service.Options{Executable: exe}
	if cfgPath != config.DefaultPath() {
		o.ConfigPath = cfgPath
	}
	if err := mgr.Install(o); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	p.OK("Service installed and started (" + mgr.Path() + ")")
	return nil
}

// isPrivateURL reports whether a URL points at a LAN/loopback host.
func isPrivateURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".home.arpa") || !strings.Contains(h, ".") && !strings.Contains(h, ":") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
