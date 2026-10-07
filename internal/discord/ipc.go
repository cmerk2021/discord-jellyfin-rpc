// Package discord implements a minimal Discord RPC client over the local IPC socket.
package discord

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2
	opPing      = 3
	opPong      = 4

	maxFrame = 64 << 10
)

// Activity types.
const (
	ActivityPlaying   = 0
	ActivityListening = 2
	ActivityWatching  = 3
	ActivityCompeting = 5
)

// Status display types (which field is shown in the member list status line).
const (
	StatusDisplayName    = 0
	StatusDisplayState   = 1
	StatusDisplayDetails = 2
)

type Timestamps struct {
	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
}

type Assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"`
	SmallText  string `json:"small_text,omitempty"`
}

type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Activity is a Discord rich presence activity.
type Activity struct {
	Type              int         `json:"type"`
	StatusDisplayType int         `json:"status_display_type"`
	Details           string      `json:"details,omitempty"`
	DetailsURL        string      `json:"details_url,omitempty"`
	State             string      `json:"state,omitempty"`
	StateURL          string      `json:"state_url,omitempty"`
	Timestamps        *Timestamps `json:"timestamps,omitempty"`
	Assets            *Assets     `json:"assets,omitempty"`
	Buttons           []Button    `json:"buttons,omitempty"`
}

// ErrNotRunning is returned when no Discord IPC socket could be found.
var ErrNotRunning = errors.New("discord: no running Discord client found")

// RPCError is an error returned by Discord for a command.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Client is a connection to the local Discord client.
type Client struct {
	clientID string
	mu       sync.Mutex
	conn     net.Conn
	user     string
}

// New creates a client for the given application ID.
func New(clientID string) *Client { return &Client{clientID: clientID} }

// Connected reports whether the IPC connection is open.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// User returns the username reported by Discord on connect.
func (c *Client) User() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.user
}

// SocketCandidates lists IPC socket paths to try, in order. Setting JELLYFIN_RPC_DISCORD_IPC
// to a socket path disables discovery and uses only that path.
func SocketCandidates() []string {
	if p := os.Getenv("JELLYFIN_RPC_DISCORD_IPC"); p != "" {
		return []string{p}
	}
	var bases []string
	seen := map[string]bool{}
	for _, k := range []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"} {
		if v := os.Getenv(k); v != "" && !seen[v] {
			seen[v] = true
			bases = append(bases, v)
		}
	}
	if runtime.GOOS == "linux" {
		if d := fmt.Sprintf("/run/user/%d", os.Getuid()); !seen[d] {
			seen[d] = true
			bases = append(bases, d)
		}
	}
	if !seen["/tmp"] {
		bases = append(bases, "/tmp")
	}
	subdirs := []string{
		"",
		"app/com.discordapp.Discord",       // Flatpak
		"app/com.discordapp.DiscordCanary", // Flatpak canary
		"app/dev.vencord.Vesktop",          // Vesktop Flatpak
		"app/xyz.armcord.ArmCord",          // ArmCord Flatpak
		".flatpak/com.discordapp.Discord/xdg-run",
		".flatpak/dev.vencord.Vesktop/xdg-run",
		"snap.discord",
		"snap.discord-canary",
	}
	var out []string
	for i := range 10 {
		for _, b := range bases {
			for _, s := range subdirs {
				out = append(out, filepath.Join(b, s, fmt.Sprintf("discord-ipc-%d", i)))
			}
		}
	}
	return out
}

// Connect opens the IPC socket and performs the handshake.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	if c.clientID == "" {
		return errors.New("discord: no client ID configured")
	}
	var lastErr error = ErrNotRunning
	for _, p := range SocketCandidates() {
		if fi, err := os.Stat(p); err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		conn, err := dial(p)
		if err != nil {
			lastErr = fmt.Errorf("discord: dial %s: %w", p, err)
			continue
		}
		user, err := handshake(conn, c.clientID)
		if err != nil {
			conn.Close()
			lastErr = err
			continue
		}
		c.conn, c.user = conn, user
		return nil
	}
	return lastErr
}

func dial(p string) (net.Conn, error) {
	return net.DialTimeout("unix", p, 3*time.Second)
}

func handshake(conn net.Conn, clientID string) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if err := writeFrame(conn, opHandshake, map[string]any{"v": 1, "client_id": clientID}); err != nil {
		return "", fmt.Errorf("discord: handshake: %w", err)
	}
	op, data, err := readFrame(conn)
	if err != nil {
		return "", fmt.Errorf("discord: handshake: %w", err)
	}
	if op == opClose {
		var e RPCError
		_ = json.Unmarshal(data, &e)
		return "", fmt.Errorf("discord: handshake rejected: %w", &e)
	}
	var ready struct {
		Cmd  string `json:"cmd"`
		Evt  string `json:"evt"`
		Data struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &ready); err != nil {
		return "", fmt.Errorf("discord: handshake: %w", err)
	}
	if ready.Evt != "READY" {
		return "", fmt.Errorf("discord: unexpected handshake response %s/%s", ready.Cmd, ready.Evt)
	}
	return ready.Data.User.Username, nil
}

// SetActivity sets (or clears, when a is nil) the rich presence.
func (c *Client) SetActivity(a *Activity) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("discord: not connected")
	}
	nonce := newNonce()
	args := map[string]any{"pid": os.Getpid()}
	if a != nil {
		args["activity"] = a
	} else {
		args["activity"] = nil
	}
	payload := map[string]any{"cmd": "SET_ACTIVITY", "args": args, "nonce": nonce}
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		if c.conn != nil {
			_ = c.conn.SetDeadline(time.Time{})
		}
	}()
	if err := writeFrame(c.conn, opFrame, payload); err != nil {
		c.closeLocked()
		return fmt.Errorf("discord: write: %w", err)
	}
	for {
		op, data, err := readFrame(c.conn)
		if err != nil {
			c.closeLocked()
			return fmt.Errorf("discord: read: %w", err)
		}
		switch op {
		case opPing:
			if err := writeRaw(c.conn, opPong, data); err != nil {
				c.closeLocked()
				return fmt.Errorf("discord: pong: %w", err)
			}
			continue
		case opClose:
			var e RPCError
			_ = json.Unmarshal(data, &e)
			c.closeLocked()
			return fmt.Errorf("discord: connection closed: %w", &e)
		case opFrame:
		default:
			continue
		}
		var resp struct {
			Evt   string    `json:"evt"`
			Nonce string    `json:"nonce"`
			Data  *RPCError `json:"data"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.Nonce != nonce {
			continue
		}
		if resp.Evt == "ERROR" && resp.Data != nil {
			return resp.Data
		}
		return nil
	}
}

// Close closes the connection, if any.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = writeRaw(c.conn, opClose, []byte("{}"))
	}
	c.closeLocked()
	return nil
}

func (c *Client) closeLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func writeFrame(w io.Writer, op uint32, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeRaw(w, op, b)
}

func writeRaw(w io.Writer, op uint32, b []byte) error {
	buf := make([]byte, 8+len(b))
	binary.LittleEndian.PutUint32(buf[0:4], op)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(b)))
	copy(buf[8:], b)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (uint32, []byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := binary.LittleEndian.Uint32(hdr[0:4])
	n := binary.LittleEndian.Uint32(hdr[4:8])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large (%d bytes)", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	return op, data, nil
}

func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
