//go:build integration

package jellyfin

// Integration test against a real, fresh Jellyfin server (see .github/workflows/ci.yml).
// Run locally with:
//   docker run -d -p 8096:8096 jellyfin/jellyfin:latest
//   JELLYFIN_TEST_URL=http://127.0.0.1:8096 go test -tags integration ./internal/jellyfin/

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestIntegrationJellyfin(t *testing.T) {
	base := os.Getenv("JELLYFIN_TEST_URL")
	if base == "" {
		t.Skip("JELLYFIN_TEST_URL not set")
	}
	user, pw := envOr("JELLYFIN_TEST_USER", "rpc-test"), envOr("JELLYFIN_TEST_PASSWORD", "rpc-test-pw")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := New(Options{BaseURL: base, DeviceID: "integration-test", Version: "0.0.0"})

	var info *PublicSystemInfo
	var err error
	for i := 0; i < 60; i++ {
		if info, err = c.PublicInfo(ctx); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		t.Fatalf("server never became ready: %v", err)
	}
	t.Logf("Jellyfin %s", info.Version)

	if !info.StartupWizardCompleted {
		for _, step := range []struct {
			path string
			body any
		}{
			{"/Startup/Configuration", map[string]string{"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"}},
			{"/Startup/User", map[string]string{"Name": user, "Password": pw}},
			{"/Startup/Complete", nil},
		} {
			if step.path == "/Startup/User" {
				_ = c.do(ctx, http.MethodGet, "/Startup/User", nil, nil, nil)
			}
			if err := c.do(ctx, http.MethodPost, step.path, nil, step.body, nil); err != nil {
				t.Fatalf("startup %s: %v", step.path, err)
			}
		}
	}

	res, err := c.AuthenticateByName(ctx, user, pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	c.SetToken(res.AccessToken)
	me, err := c.Me(ctx)
	if err != nil || me.ID == "" {
		t.Fatalf("me: %+v %v", me, err)
	}
	if _, err := c.Sessions(ctx); err != nil {
		t.Fatalf("sessions: %v", err)
	}

	// Requests without a token must be rejected, proving the token in our header is what's used.
	anon := New(Options{BaseURL: base, DeviceID: "anon"})
	if _, err := anon.Sessions(ctx); !IsUnauthorized(err) {
		t.Fatalf("expected unauthenticated /Sessions to fail with 401, got %v", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
