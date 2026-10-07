# jellyfin-rpc

Show what you're watching on Jellyfin as your Discord status. Your profile shows **"Watching The Rookie"** (the show name, not "Jellyfin"), plus the episode, a live progress bar, and the show's cover art.

```
┌──────────────────────────────────────────┐
│ Watching The Rookie                      │
│ ┌──────┐  The Rookie                     │
│ │cover │  S01E05 · The Roundup           │
│ │ art  │  12:04 ━━━━━━━━○──────── 43:10  │
│ └──────┘                                 │
└──────────────────────────────────────────┘
```

- **Shows:** series name, `SxxExx · Episode name`, and series/season/episode cover.
- **Movies:** title, year · genres, poster, and IMDb/TMDB buttons.
- **Music and audiobooks:** "Listening to *Artist*", with track, album art and progress.
- **Music videos and Live TV:** also supported.
- **Configurable:** Go templates for every line, per media type.
- **Lightweight:** a single static Go binary with no runtime dependencies, using about 10 MB of RAM and near-zero CPU.
- **Resilient:** keeps working if Jellyfin goes down, the network drops, or Discord restarts. It backs off, retries, and reconnects on its own. It also runs as a systemd user service (Linux) or launchd agent (macOS), which restart it if it crashes.
- **Jellyfin 12 ready:** uses only the `Authorization: MediaBrowser …` header and the current API routes. Tested against Jellyfin 12.2; CI also runs against 10.10.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/cmerk2021/discord-jellyfin-rpc/main/install.sh | sh
```

The installer:
1. Detects your OS and CPU (Linux/macOS, amd64/arm64/armv7) and installs `curl`/`tar` if they're missing.
2. Downloads the latest release and verifies its SHA-256 checksum.
3. Installs the binary to `~/.local/bin`.
4. Starts the **setup wizard**. The wizard connects to your server, signs in, and asks what to show. It can then send a test status to Discord and install the background service so the app starts at login.

Installer options (pass them after `sh -s --`):

```sh
curl -fsSL …/install.sh | sh -s -- --version v1.2.0   # specific version
curl -fsSL …/install.sh | sh -s -- --system           # /usr/local/bin (sudo)
curl -fsSL …/install.sh | sh -s -- --no-setup         # just install the binary
curl -fsSL …/install.sh | sh -s -- --uninstall        # remove service + binary (+ config)
```

Re-running the installer upgrades in place and restarts the service if it's running.

Prefer not to pipe to `sh`? Download the archive for your platform from [Releases](https://github.com/cmerk2021/discord-jellyfin-rpc/releases), put `jellyfin-rpc` on your `PATH`, and run `jellyfin-rpc setup`. To build from source: `go install github.com/cmerk2021/discord-jellyfin-rpc/cmd/jellyfin-rpc@latest`.

## Usage

```
jellyfin-rpc [run]              Run in the foreground (default)
jellyfin-rpc setup              Interactive configuration wizard
jellyfin-rpc check              Test Jellyfin/Discord and print the current presence
jellyfin-rpc service <cmd>      install | uninstall | status | restart
jellyfin-rpc config path        Print the config file location
jellyfin-rpc config example     Print a fully commented default config
jellyfin-rpc version            Print version information
```

**Discord requirement:** the desktop Discord app must be running on the same machine. Vesktop, ArmCord, Flatpak and Snap installs are detected. "Share my activity" must be on (*User Settings → Activity Privacy*).

## Configuration

Config file location:
- Linux: `~/.config/jellyfin-rpc/config.toml`
- macOS: `~/Library/Application Support/jellyfin-rpc/config.toml`

You can override it with `--config` or `JELLYFIN_RPC_CONFIG`. The setup wizard writes a fully commented file; [`config.example.toml`](config.example.toml) shows every option. Unknown keys are rejected, so typos don't fail silently.

After editing the file, run `jellyfin-rpc service restart`, or `systemctl --user reload jellyfin-rpc` to reload without restarting (SIGHUP).

### Highlights

| Option | Description |
|---|---|
| `jellyfin.url` | Server URL used by jellyfin-rpc. |
| `jellyfin.public_url` | Internet-reachable URL used for **cover art**. Discord fetches images itself, so a LAN address won't work. If empty, a generic Jellyfin logo is shown instead. |
| `jellyfin.auth_method` | `password` stores only a session token (shown in Dashboard → Devices). `api_key` uses an admin API key. |
| `jellyfin.clients` / `devices` / `ignore_clients` | Only show (or never show) sessions from certain apps or devices. |
| `jellyfin.ignore_libraries` | Hide items from these libraries, e.g. `["Home Videos"]`. |
| `behavior.media_types` | `episode`, `movie`, `audio`, `musicvideo`, `livetv`, `other`. |
| `behavior.paused` | `clear` hides the status when paused; `show` keeps it, without the progress bar. |
| `behavior.poll_interval` | How often to poll while playing (default `5s`; `15s` when idle). |
| `images.episode_source` | Cover for episodes: `series`, `season`, or `episode` (thumbnail). |
| `discord.client_id` | Discord application to show as. See *Using your own Discord app*. |

Environment overrides (handy for containers or secrets): `JELLYFIN_RPC_URL`, `JELLYFIN_RPC_PUBLIC_URL`, `JELLYFIN_RPC_API_KEY`, `JELLYFIN_RPC_TOKEN`, `JELLYFIN_RPC_USER_ID`, `JELLYFIN_RPC_CLIENT_ID`, `JELLYFIN_RPC_LOG_LEVEL`.

### Templates

Each media type has its own `[templates.<type>]` table, written with [Go templates](https://pkg.go.dev/text/template). These are the defaults for episodes:

```toml
[templates.episode]
activity_type  = "watching"   # playing | listening | watching | competing
status_display = "details"    # status line shows "Watching <details>"
details    = "{{.SeriesName}}"
state      = "{{with epcode .}}{{.}} · {{end}}{{.Name}}"
large_text = "{{.SeriesName}}{{with .SeasonName}} · {{.}}{{end}}"
small_text = "{{if .Paused}}Paused{{else}}Playing{{end}}"
buttons    = []
```

How the fields work:
- `status_display` controls what follows "Watching" in your member-list status: `name` (the Discord app's name), `details`, or `state`.
- A line whose template renders empty is hidden.
- A button is skipped when its URL renders empty, such as a missing IMDb ID.
- Text is trimmed to Discord's limits automatically.

Example: show the season name and a percentage instead:

```toml
[templates.episode]
state = "{{.SeasonName}} · Episode {{.Episode}} ({{percent .Position .Runtime}}%)"
```

**Fields:** `.Name .OriginalTitle .SeriesName .SeasonName .Season .Episode .EpisodeEnd .Year .Album .Artist .Artists .AlbumArtist .Genres .Studios .OfficialRating .CommunityRating .Paused .Position .Runtime .Providers .Type .MediaType .ItemID .SeriesID .User .Client .Device .ServerURL .WebURL`

**Functions:**

| Function | Example | Result |
|---|---|---|
| `epcode .` | `{{epcode .}}` | `S01E05`, `S01E05-E06` |
| `pad` / `pad3` | `{{pad .Episode}}` | `05` / `005` |
| `provider . "imdb"` | IMDb/TMDB/TVDB/MusicBrainz ID | `tt7587890` |
| `join list sep` | `{{join .Genres ", "}}` | `Drama, Crime` |
| `limit list n` | `{{join (limit .Genres 2) "/"}}` | `Drama/Crime` |
| `default val fallback` | `{{default .Artist "Unknown"}}` | |
| `trunc n str`, `upper`, `lower`, `title` | | |
| `duration d` | `{{duration .Runtime}}` | `43:10`, `1:52:03` |
| `percent pos total` | `{{percent .Position .Runtime}}%` | `27%` |

## Using your own Discord app

Release builds include a default Discord application ID. To use your own name or icons:

1. Create an application at <https://discord.com/developers/applications>. Its name is what `status_display = "name"` shows.
2. Under **Rich Presence → Art Assets**, upload images named `jellyfin` (the fallback cover), `play`, and `pause`.
3. Put the **Application ID** in `discord.client_id`.

## Background service

`jellyfin-rpc service install` sets up autostart at login:

| | Linux (systemd user unit) | macOS (launchd agent) |
|---|---|---|
| File | `~/.config/systemd/user/jellyfin-rpc.service` | `~/Library/LaunchAgents/io.github.cmerk2021.jellyfin-rpc.plist` |
| Restart on crash | `Restart=always` | `KeepAlive` |
| Logs | `journalctl --user -u jellyfin-rpc -f` | `~/Library/Logs/jellyfin-rpc.log` |

On Linux headless or SSH setups, run `loginctl enable-linger` if you want the service to start without logging in. It still needs a running Discord, though.

## Troubleshooting

- **Run `jellyfin-rpc check` first.** It tests the Jellyfin connection and login, prints exactly what would be shown, and tries to reach Discord.
- **Nothing shows in Discord:** make sure the desktop app is running and *Share my activity* is enabled. Browser Discord can't be detected.
- **No cover art:** set `jellyfin.public_url` to an address reachable from the internet. Open `<public_url>/Items/<id>/Images/Primary` in a private browser window to confirm it loads without logging in.
- **"Invalid Client ID":** the `discord.client_id` is wrong.
- **401 Unauthorized in logs:** the session token was revoked (e.g. in Dashboard → Devices). Run `jellyfin-rpc setup` again.
- **More detail:** set `[log] level = "debug"`.

## Jellyfin 12 compatibility

Jellyfin 12 removed several legacy authentication methods:
- the `X-Emby-Token` and `X-MediaBrowser-Token` headers,
- the `api_key` query parameter,
- the `/emby` and `/mediabrowser` route prefixes.

jellyfin-rpc uses only the `Authorization: MediaBrowser Token="…", Client=…, Device=…, DeviceId=…, Version=…` header and the current routes. These also work on Jellyfin 10.9 and newer.

Cover art comes from the image endpoint (`/Items/{id}/Images/Primary`), which needs no authentication, so no token is ever included in the image URLs sent to Discord.

## Development

```sh
go test ./...                                   # unit tests (fixtures captured from Jellyfin 12.2)
docker run -d -p 8096:8096 jellyfin/jellyfin    # then:
JELLYFIN_TEST_URL=http://127.0.0.1:8096 go test -tags integration ./internal/jellyfin/
```

To publish a release, push a `v*` tag. GoReleaser builds static binaries for Linux (amd64/arm64/armv7) and macOS (amd64/arm64). Set the `DISCORD_CLIENT_ID` repository variable to bake in the default application ID.

## License

See [LICENSE](LICENSE).
