package service

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	u, err := SystemdUnit(Options{Executable: "/home/me/.local/bin/jellyfin-rpc", ConfigPath: "/home/me/my config.toml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ExecStart=/home/me/.local/bin/jellyfin-rpc run --config "/home/me/my config.toml"`,
		"Restart=always",
		"WantedBy=default.target",
		"ExecReload=/bin/kill -HUP $MAINPID",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestLaunchdPlist(t *testing.T) {
	p, err := LaunchdPlist(Options{Executable: "/Users/me/bin/jellyfin-rpc"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>/Users/me/bin/jellyfin-rpc</string>", "<string>run</string>", "<key>KeepAlive</key>", "<key>RunAtLoad</key>", label} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if strings.Contains(p, "--config") {
		t.Error("default config path should not be passed")
	}
}

func TestSystemdQuote(t *testing.T) {
	if got := systemdQuote(`a$b%c"d`); got != `"a$$b%%c\"d"` {
		t.Fatalf("got %s", got)
	}
}
