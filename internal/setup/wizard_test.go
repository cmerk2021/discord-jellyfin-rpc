package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
)

func fakeJellyfin(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "" || strings.HasPrefix(r.URL.Path, "/emby") {
			t.Errorf("legacy API usage: %s", r.URL)
		}
		switch r.URL.Path {
		case "/System/Info/Public":
			w.Write([]byte(`{"serverName":"Home","version":"12.2.0"}`))
		case "/Users/AuthenticateByName":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["Pw"] != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"User":{"Id":"uid1","Name":"bob"},"AccessToken":"tok1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestWizardPasswordFlow(t *testing.T) {
	srv := fakeJellyfin(t)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "config.toml")
	input := strings.Join([]string{
		srv.URL + "/web/", // server URL (trailing /web/ stripped)
		"https://jf.example.com",
		"1",            // username & password
		"bob", "wrong", // bad password
		"y", // try again
		"bob", "pw",
		"", "", "n", "", "", "", // media types: disable audio
		"2",          // keep showing when paused
		"n",          // no buttons
		"abc", "123", // invalid then valid client id
		"n", // skip discord test
	}, "\n") + "\n"
	var out bytes.Buffer
	p := NewPrompterFrom(strings.NewReader(input), &out)
	if err := run(context.Background(), p, Options{ConfigPath: path}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	j := cfg.Jellyfin
	if j.URL != srv.URL || j.PublicURL != "https://jf.example.com" || j.Token != "tok1" || j.UserID != "uid1" || j.AuthMethod != config.AuthPassword {
		t.Errorf("jellyfin config: %+v", j)
	}
	if j.DeviceID == "" {
		t.Error("device id not generated")
	}
	if cfg.MediaEnabled(config.MediaAudio) || !cfg.MediaEnabled(config.MediaEpisode) {
		t.Errorf("media types: %v", cfg.Behavior.MediaTypes)
	}
	if cfg.Behavior.Paused != config.PausedShow || len(cfg.Templates.Movie.Buttons) != 0 || cfg.Discord.ClientID != "123" {
		t.Errorf("behavior/buttons/client: %+v %+v %s", cfg.Behavior, cfg.Templates.Movie.Buttons, cfg.Discord.ClientID)
	}
	if !strings.Contains(out.String(), "Invalid username or password") || !strings.Contains(out.String(), "Jellyfin 12.2.0") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.5:8096":            "http://192.168.1.5:8096",
		"https://jf.example.com/web/": "https://jf.example.com",
		"https://x.com/jellyfin/":     "https://x.com/jellyfin",
	} {
		if got, err := normalizeURL(in); err != nil || got != want {
			t.Errorf("normalizeURL(%q) = %q, %v", in, got, err)
		}
	}
	if !versionAtLeast("12.2.0", 10, 10) || !versionAtLeast("10.10.7", 10, 10) || versionAtLeast("10.9.11", 10, 10) {
		t.Error("versionAtLeast")
	}
	for u, want := range map[string]bool{
		"http://192.168.1.2:8096":      true,
		"http://localhost:8096":        true,
		"http://nas:8096":              true,
		"http://media.local":           true,
		"https://jellyfin.example.com": false,
		"http://8.8.8.8":               false,
	} {
		if got := isPrivateURL(u); got != want {
			t.Errorf("isPrivateURL(%s) = %v", u, got)
		}
	}
}
