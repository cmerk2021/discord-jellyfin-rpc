package discord

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDiscord serves one IPC connection on a unix socket in dir.
func fakeDiscord(t *testing.T, dir string, handle func(conn net.Conn)) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, "discord-ipc-0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				handle(conn)
			}()
		}
	}()
}

func shortTempDir(t *testing.T) string {
	// unix socket paths are limited to ~104 bytes, so avoid t.TempDir() on macOS.
	dir, err := os.MkdirTemp("/tmp", "drpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func useSocketDir(t *testing.T, dir string) {
	t.Setenv("JELLYFIN_RPC_DISCORD_IPC", filepath.Join(dir, "discord-ipc-0"))
}

func TestHandshakeAndSetActivity(t *testing.T) {
	dir := shortTempDir(t)
	useSocketDir(t, dir)
	got := make(chan map[string]any, 2)
	fakeDiscord(t, dir, func(conn net.Conn) {
		op, data, err := readFrame(conn)
		if err != nil || op != opHandshake {
			t.Errorf("handshake: op=%d err=%v", op, err)
			return
		}
		var hs map[string]any
		_ = json.Unmarshal(data, &hs)
		if hs["client_id"] != "123" {
			t.Errorf("client id %v", hs["client_id"])
		}
		_ = writeFrame(conn, opFrame, map[string]any{"cmd": "DISPATCH", "evt": "READY", "data": map[string]any{"user": map[string]any{"username": "alice"}}})
		for {
			op, data, err := readFrame(conn)
			if err != nil {
				return
			}
			if op == opClose {
				return
			}
			var req map[string]any
			_ = json.Unmarshal(data, &req)
			got <- req
			// Interleave a ping and an unrelated event before the response.
			_ = writeRaw(conn, opPing, []byte(`{}`))
			if op, _, _ := readFrame(conn); op != opPong {
				t.Errorf("expected pong, got %d", op)
			}
			_ = writeFrame(conn, opFrame, map[string]any{"evt": "OTHER", "nonce": "zzz"})
			args := req["args"].(map[string]any)
			if act, ok := args["activity"].(map[string]any); ok && act["details"] == "bad" {
				_ = writeFrame(conn, opFrame, map[string]any{"cmd": "SET_ACTIVITY", "evt": "ERROR", "nonce": req["nonce"], "data": map[string]any{"code": 4000, "message": "invalid"}})
				continue
			}
			_ = writeFrame(conn, opFrame, map[string]any{"cmd": "SET_ACTIVITY", "nonce": req["nonce"], "data": map[string]any{}})
		}
	})

	c := New("123")
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if c.User() != "alice" || !c.Connected() {
		t.Fatalf("user=%q connected=%v", c.User(), c.Connected())
	}
	act := &Activity{Type: ActivityWatching, StatusDisplayType: StatusDisplayDetails, Details: "The Rookie", State: "S01E05",
		Timestamps: &Timestamps{Start: 1, End: 2}}
	if err := c.SetActivity(act); err != nil {
		t.Fatal(err)
	}
	req := <-got
	if req["cmd"] != "SET_ACTIVITY" {
		t.Fatalf("cmd %v", req["cmd"])
	}
	a := req["args"].(map[string]any)["activity"].(map[string]any)
	if a["type"].(float64) != 3 || a["status_display_type"].(float64) != 2 || a["details"] != "The Rookie" {
		t.Fatalf("activity %v", a)
	}

	err := c.SetActivity(&Activity{Details: "bad"})
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != 4000 || !c.Connected() {
		t.Fatalf("expected RPC error and still connected, got %v connected=%v", err, c.Connected())
	}
	<-got

	if err := c.SetActivity(nil); err != nil {
		t.Fatal(err)
	}
	req = <-got
	if v, ok := req["args"].(map[string]any)["activity"]; !ok || v != nil {
		t.Fatalf("clear should send activity:null, got %v", req["args"])
	}
	c.Close()
	if c.Connected() {
		t.Fatal("still connected after Close")
	}
}

func TestConnectNoDiscord(t *testing.T) {
	dir := shortTempDir(t)
	useSocketDir(t, dir)
	c := New("123")
	if err := c.Connect(); !errors.Is(err, ErrNotRunning) || c.Connected() {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestConnectionDropDetected(t *testing.T) {
	dir := shortTempDir(t)
	useSocketDir(t, dir)
	fakeDiscord(t, dir, func(conn net.Conn) {
		_, _, _ = readFrame(conn)
		_ = writeFrame(conn, opFrame, map[string]any{"evt": "READY"})
		// Then hang up.
	})
	c := New("1")
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetActivity(&Activity{Details: "x"}); err == nil {
		t.Fatal("expected error after server hung up")
	}
	if c.Connected() {
		t.Fatal("client should mark itself disconnected")
	}
}

func TestHandshakeRejected(t *testing.T) {
	dir := shortTempDir(t)
	useSocketDir(t, dir)
	fakeDiscord(t, dir, func(conn net.Conn) {
		_, _, _ = readFrame(conn)
		_ = writeFrame(conn, opClose, map[string]any{"code": 4000, "message": "Invalid Client ID"})
	})
	err := New("bad").Connect()
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != 4000 {
		t.Fatalf("expected rejection, got %v", err)
	}
}

func TestSocketCandidatesDiscovery(t *testing.T) {
	t.Setenv("JELLYFIN_RPC_DISCORD_IPC", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	c := SocketCandidates()
	if c[0] != "/run/user/4242/discord-ipc-0" {
		t.Fatalf("first candidate %s", c[0])
	}
	var flatpak, snap bool
	for _, p := range c {
		flatpak = flatpak || strings.Contains(p, "app/com.discordapp.Discord/discord-ipc-0")
		snap = snap || strings.Contains(p, "snap.discord/discord-ipc-0")
	}
	if !flatpak || !snap {
		t.Fatal("flatpak/snap paths missing")
	}
}
