// Command jellyfin-rpc shows your Jellyfin playback as Discord Rich Presence.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cmerk2021/discord-jellyfin-rpc/internal/app"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/config"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/discord"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/service"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/setup"
	"github.com/cmerk2021/discord-jellyfin-rpc/internal/version"
)

const usage = `jellyfin-rpc - Jellyfin → Discord Rich Presence

Usage:
  jellyfin-rpc [run]              Run in the foreground (default)
  jellyfin-rpc setup              Interactive configuration wizard
  jellyfin-rpc check              Test Jellyfin/Discord and print the current presence
  jellyfin-rpc service <cmd>      install | uninstall | status | restart
  jellyfin-rpc config path        Print the config file location
  jellyfin-rpc config example     Print a fully commented default config
  jellyfin-rpc version            Print version information

Global flags:
  --config PATH   Config file (default: %s)
                  Can also be set with JELLYFIN_RPC_CONFIG.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("jellyfin-rpc", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, config.DefaultPath()) }
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	noService := fs.Bool("no-service-prompt", false, "setup: don't offer to install the background service")
	if err := fs.Parse(reorder(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	rest := fs.Args()
	cmd := "run"
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}
	switch cmd {
	case "run":
		return cmdRun(*cfgPath)
	case "setup":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return setup.Run(ctx, setup.Options{ConfigPath: *cfgPath, ServicePrompt: !*noService})
	case "check":
		return cmdCheck(*cfgPath)
	case "service":
		return cmdService(*cfgPath, rest)
	case "config":
		return cmdConfig(*cfgPath, rest)
	case "version", "--version", "-v":
		fmt.Printf("jellyfin-rpc %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return nil
	case "help":
		fs.Usage()
		return nil
	}
	fs.Usage()
	return fmt.Errorf("unknown command %q", cmd)
}

// reorder moves flags before positional args so `jellyfin-rpc run --config x` works.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && (a == "--config" || a == "-config") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func newLogger(cfg *config.Config) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.Log.Level))
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func cmdRun(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	a, err := app.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	reload := make(chan struct{}, 1)
	go func() {
		for range hup {
			select {
			case reload <- struct{}{}:
			default:
			}
		}
	}()
	return a.Run(ctx, reload, func() (*config.Config, error) { return config.Load(path) })
}

func cmdCheck(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	fmt.Println("config:  ", path, "✔")
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		return err
	}
	jf := app.NewJellyfin(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, err := jf.PublicInfo(ctx)
	if err != nil {
		fmt.Println("jellyfin:", "✘", err)
	} else {
		fmt.Printf("jellyfin: %s (Jellyfin %s) ✔\n", info.ServerName, info.Version)
	}
	act, err := a.Preview(ctx)
	switch {
	case err != nil:
		fmt.Println("sessions:", "✘", err)
	case act == nil:
		fmt.Println("sessions: nothing playing (that matches your filters)")
	default:
		b, _ := json.MarshalIndent(act, "          ", "  ")
		fmt.Println("presence:", string(b))
	}
	dc := discord.New(cfg.Discord.ClientID)
	if err := dc.Connect(); err != nil {
		fmt.Println("discord: ", "✘", err)
	} else {
		fmt.Printf("discord:  connected as %s ✔\n", dc.User())
		_ = dc.Close()
	}
	if mgr, err := service.New(); err == nil {
		state := "not installed"
		if mgr.Installed() {
			state = "installed (" + mgr.Path() + ")"
		}
		fmt.Println("service: ", state)
	}
	return nil
}

func cmdService(cfgPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: jellyfin-rpc service install|uninstall|status|restart")
	}
	mgr, err := service.New()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		if _, err := config.Load(cfgPath); err != nil {
			return fmt.Errorf("refusing to install service with invalid config: %w", err)
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
			return err
		}
		fmt.Println("installed and started:", mgr.Path())
	case "uninstall":
		if !mgr.Installed() {
			fmt.Println("service is not installed")
			return nil
		}
		if err := mgr.Uninstall(); err != nil {
			return err
		}
		fmt.Println("service removed")
	case "status":
		out, err := mgr.Status()
		if err != nil {
			return err
		}
		fmt.Println(out)
		if !mgr.Running() {
			os.Exit(3) // like `systemctl status`: lets scripts detect a stopped service
		}
	case "restart":
		return mgr.Restart()
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
	return nil
}

func cmdConfig(cfgPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: jellyfin-rpc config path|example")
	}
	switch args[0] {
	case "path":
		fmt.Println(cfgPath)
	case "example":
		cfg := config.Default()
		cfg.Jellyfin.URL = "http://192.168.1.10:8096"
		cfg.Jellyfin.PublicURL = "https://jellyfin.example.com"
		cfg.Discord.ClientID = config.DefaultClientID
		b, err := cfg.Render()
		if err != nil {
			return err
		}
		os.Stdout.Write(b)
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
	return nil
}
