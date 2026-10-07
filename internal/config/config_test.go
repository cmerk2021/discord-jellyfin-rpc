package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func valid() *Config {
	c := Default()
	c.Jellyfin.URL = "http://jf.lan:8096/"
	c.Jellyfin.Token = "tok"
	c.Discord.ClientID = "123"
	return c
}

func TestDefaultsValidate(t *testing.T) {
	c := valid()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Jellyfin.URL != "http://jf.lan:8096" {
		t.Errorf("trailing slash not trimmed: %s", c.Jellyfin.URL)
	}
}

func TestValidateErrors(t *testing.T) {
	c := Default()
	c.Discord.ClientID = ""
	c.Behavior.Paused = "nope"
	c.Behavior.MediaTypes = []string{"podcast"}
	c.Templates.Movie.Buttons = make([]Button, 3)
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"jellyfin.url", "jellyfin.token", "discord.client_id", "behavior.paused", "podcast", "at most 2 buttons"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	c := valid()
	c.Jellyfin.PublicURL = "https://jf.example.com"
	c.Jellyfin.Clients = []string{`Weird "client"`}
	c.Jellyfin.DeviceID = "dev\\id"
	c.Templates.Episode.Buttons = nil // disabled buttons must survive a round trip
	c.Behavior.PollInterval = D(7 * time.Second)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config perms %v", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		b, _ := os.ReadFile(path)
		t.Fatalf("%v\n%s", err, b)
	}
	if len(got.Templates.Episode.Buttons) != 0 {
		t.Errorf("episode buttons came back: %+v", got.Templates.Episode.Buttons)
	}
	got.Templates.Episode.Buttons, c.Templates.Episode.Buttons = nil, nil
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", c) {
		t.Errorf("round trip mismatch\n got %+v\nwant %+v", got, c)
	}
}

func TestPartialTemplateOverrideKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(path, []byte(`
[jellyfin]
url = "http://x"
token = "t"
[discord]
client_id = "1"
[templates.episode]
state = "{{.Name}}"
`), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Templates.Episode.State != "{{.Name}}" || c.Templates.Episode.Details != DefaultTemplates().Episode.Details {
		t.Fatalf("partial override broke defaults: %+v", c.Templates.Episode)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(path, []byte("[jellyfin]\nurl = \"http://x\"\ntoken=\"t\"\ntypo = 1\n[discord]\nclient_id=\"1\"\n"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "jellyfin.typo") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(path, []byte("[jellyfin]\nurl = \"http://x\"\n[discord]\nclient_id=\"1\"\n"), 0o600)
	t.Setenv("JELLYFIN_RPC_TOKEN", "from-env")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Jellyfin.Token != "from-env" {
		t.Fatal("env override not applied")
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil || !strings.Contains(err.Error(), "setup") {
		t.Fatalf("expected helpful not-found error, got %v", err)
	}
}
