// Package service installs jellyfin-rpc as a per-user background service
// (systemd --user on Linux, a LaunchAgent on macOS).
package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

const (
	unitName = "jellyfin-rpc.service"
	label    = "io.github.cmerk2021.jellyfin-rpc"
)

// Options configures the generated service.
type Options struct {
	Executable string
	ConfigPath string // empty = default location
}

// Manager installs and controls the service.
type Manager interface {
	Install(Options) error
	Uninstall() error
	Status() (string, error)
	Restart() error
	Installed() bool
	Running() bool
	Path() string
}

// ErrUnsupported is returned on platforms without a supported service manager.
var ErrUnsupported = errors.New("background service is only supported on Linux (systemd) and macOS (launchd)")

// New returns the manager for the current OS.
func New() (Manager, error) {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return nil, fmt.Errorf("%w: systemctl not found", ErrUnsupported)
		}
		return &systemd{}, nil
	case "darwin":
		return &launchd{}, nil
	}
	return nil, ErrUnsupported
}

// Executable returns the resolved path of the running binary.
func Executable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Abs(p)
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
		}
		return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return s, nil
}

func args(o Options) []string {
	a := []string{o.Executable, "run"}
	if o.ConfigPath != "" {
		a = append(a, "--config", o.ConfigPath)
	}
	return a
}

// ---- systemd ----

type systemd struct{}

func (systemd) Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

var unitTmpl = template.Must(template.New("unit").Funcs(template.FuncMap{"quote": systemdQuote}).Parse(`[Unit]
Description=Jellyfin Discord Rich Presence
Documentation=https://github.com/cmerk2021/discord-jellyfin-rpc
After=graphical-session.target network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart={{range $i, $a := .}}{{if $i}} {{end}}{{quote $a}}{{end}}
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=10
Nice=10
MemoryMax=64M
NoNewPrivileges=true

[Install]
WantedBy=default.target
`))

func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$%") {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `$`, `$$`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	return `"` + s + `"`
}

// SystemdUnit renders the unit file contents.
func SystemdUnit(o Options) (string, error) {
	var b bytes.Buffer
	err := unitTmpl.Execute(&b, args(o))
	return b.String(), err
}

func (s systemd) Install(o Options) error {
	unit, err := SystemdUnit(o)
	if err != nil {
		return err
	}
	p := s.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		return err
	}
	if _, err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := run("systemctl", "--user", "enable", unitName); err != nil {
		return err
	}
	_, err = run("systemctl", "--user", "restart", unitName)
	return err
}

func (s systemd) Uninstall() error {
	_, _ = run("systemctl", "--user", "disable", "--now", unitName)
	if err := os.Remove(s.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := run("systemctl", "--user", "daemon-reload")
	return err
}

func (s systemd) Status() (string, error) {
	out, _ := run("systemctl", "--user", "--no-pager", "status", unitName)
	return out, nil
}

func (s systemd) Restart() error {
	_, err := run("systemctl", "--user", "restart", unitName)
	return err
}

func (s systemd) Running() bool {
	_, err := run("systemctl", "--user", "is-active", "--quiet", unitName)
	return err == nil
}

func (s systemd) Installed() bool {
	_, err := os.Stat(s.Path())
	return err == nil
}

// ---- launchd ----

type launchd struct{}

func (launchd) Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{x .Label}}</string>
	<key>ProgramArguments</key>
	<array>
{{- range .Args}}
		<string>{{x .}}</string>
{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{x .Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{x .Log}}</string>
</dict>
</plist>
`))

// LogPath is where the macOS LaunchAgent writes logs.
func LogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "jellyfin-rpc.log")
}

// LaunchdPlist renders the LaunchAgent plist.
func LaunchdPlist(o Options) (string, error) {
	var b bytes.Buffer
	err := plistTmpl.Execute(&b, map[string]any{"Label": label, "Args": args(o), "Log": LogPath()})
	return b.String(), err
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (l launchd) Install(o Options) error {
	plist, err := LaunchdPlist(o)
	if err != nil {
		return err
	}
	p := l.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(LogPath()), 0o755)
	_, _ = run("launchctl", "bootout", domain()+"/"+label)
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return err
	}
	_, err = run("launchctl", "bootstrap", domain(), p)
	return err
}

func (l launchd) Uninstall() error {
	_, _ = run("launchctl", "bootout", domain()+"/"+label)
	if err := os.Remove(l.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l launchd) Status() (string, error) {
	out, err := run("launchctl", "print", domain()+"/"+label)
	if err != nil {
		return "not loaded", nil
	}
	return out, nil
}

func (l launchd) Restart() error {
	_, err := run("launchctl", "kickstart", "-k", domain()+"/"+label)
	return err
}

func (l launchd) Running() bool {
	out, err := run("launchctl", "print", domain()+"/"+label)
	return err == nil && strings.Contains(out, "state = running")
}

func (l launchd) Installed() bool {
	_, err := os.Stat(l.Path())
	return err == nil
}
