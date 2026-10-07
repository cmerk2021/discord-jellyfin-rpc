// Package jellyfin is a minimal Jellyfin API client compatible with Jellyfin 10.10+ and 12.x.
//
// Jellyfin 12 removed the legacy X-Emby-Token / X-MediaBrowser-Token headers, the api_key query
// parameter and the /emby and /mediabrowser route prefixes. This client only ever authenticates
// with the "Authorization: MediaBrowser ..." header and only uses root routes.
package jellyfin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ClientName = "jellyfin-rpc"

// TicksPerSecond is the number of Jellyfin ticks (100ns) in a second.
const TicksPerSecond = 10_000_000

// TicksToDuration converts Jellyfin ticks to a time.Duration.
func TicksToDuration(t int64) time.Duration { return time.Duration(t) * 100 }

// Options configures a Client.
type Options struct {
	BaseURL            string
	Token              string
	DeviceID           string
	DeviceName         string
	Version            string
	Timeout            time.Duration
	InsecureSkipVerify bool
}

// Client talks to a Jellyfin server.
type Client struct {
	base string
	opts Options
	http *http.Client
}

// New creates a client.
func New(o Options) *Client {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.DeviceName == "" {
		o.DeviceName = "jellyfin-rpc"
	}
	if o.Version == "" {
		o.Version = "0.0.0"
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: o.Timeout,
		ForceAttemptHTTP2:     true,
	}
	if o.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in by user
	}
	return &Client{
		base: strings.TrimRight(o.BaseURL, "/"),
		opts: o,
		http: &http.Client{Transport: tr, Timeout: o.Timeout},
	}
}

// SetToken changes the access token used for requests.
func (c *Client) SetToken(t string) { c.opts.Token = t }

// CloseIdle closes idle keep-alive connections.
func (c *Client) CloseIdle() { c.http.CloseIdleConnections() }

func headerValue(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '"' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, s)
	return url.PathEscape(s)
}

// AuthorizationHeader builds the Jellyfin 12-compatible Authorization header value.
func (c *Client) AuthorizationHeader() string {
	parts := []string{
		fmt.Sprintf(`Client="%s"`, headerValue(ClientName)),
		fmt.Sprintf(`Device="%s"`, headerValue(c.opts.DeviceName)),
		fmt.Sprintf(`DeviceId="%s"`, headerValue(c.opts.DeviceID)),
		fmt.Sprintf(`Version="%s"`, headerValue(c.opts.Version)),
	}
	if c.opts.Token != "" {
		parts = append(parts, fmt.Sprintf(`Token="%s"`, headerValue(c.opts.Token)))
	}
	return "MediaBrowser " + strings.Join(parts, ", ")
}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	Code   int
	Method string
	Path   string
	Body   string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("jellyfin: %s %s: HTTP %d", e.Method, e.Path, e.Code)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// IsUnauthorized reports whether err is a 401/403 from Jellyfin.
func IsUnauthorized(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.AuthorizationHeader())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", ClientName+"/"+c.opts.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Method: method, Path: path, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	// Bound the response size to keep memory usage predictable.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("jellyfin: decode %s: %w", path, err)
	}
	return nil
}

// PublicSystemInfo is the unauthenticated server info.
type PublicSystemInfo struct {
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	ID                     string `json:"Id"`
	LocalAddress           string `json:"LocalAddress"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// PublicInfo fetches /System/Info/Public.
func (c *Client) PublicInfo(ctx context.Context) (*PublicSystemInfo, error) {
	var out PublicSystemInfo
	if err := c.do(ctx, http.MethodGet, "/System/Info/Public", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// User is a Jellyfin user.
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// AuthResult is the result of /Users/AuthenticateByName.
type AuthResult struct {
	User        User   `json:"User"`
	AccessToken string `json:"AccessToken"`
	ServerID    string `json:"ServerId"`
}

// AuthenticateByName logs in with a username and password.
func (c *Client) AuthenticateByName(ctx context.Context, username, password string) (*AuthResult, error) {
	var out AuthResult
	err := c.do(ctx, http.MethodPost, "/Users/AuthenticateByName", nil,
		map[string]string{"Username": username, "Pw": password}, &out)
	if err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("jellyfin: login succeeded but no access token returned")
	}
	return &out, nil
}

// Me returns the user that owns the current token (user tokens only, not API keys).
func (c *Client) Me(ctx context.Context) (*User, error) {
	var out User
	if err := c.do(ctx, http.MethodGet, "/Users/Me", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Users lists users (requires admin token or API key).
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var out []User
	if err := c.do(ctx, http.MethodGet, "/Users", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// NameID is a name/id pair.
type NameID struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

// Item is the subset of BaseItemDto used for presence.
type Item struct {
	ID                    string            `json:"Id"`
	Name                  string            `json:"Name"`
	OriginalTitle         string            `json:"OriginalTitle"`
	Type                  string            `json:"Type"`
	MediaType             string            `json:"MediaType"`
	SeriesName            string            `json:"SeriesName"`
	SeriesID              string            `json:"SeriesId"`
	SeriesPrimaryImageTag string            `json:"SeriesPrimaryImageTag"`
	SeasonID              string            `json:"SeasonId"`
	SeasonName            string            `json:"SeasonName"`
	ParentIndexNumber     *int              `json:"ParentIndexNumber"`
	IndexNumber           *int              `json:"IndexNumber"`
	IndexNumberEnd        *int              `json:"IndexNumberEnd"`
	ProductionYear        int               `json:"ProductionYear"`
	RunTimeTicks          int64             `json:"RunTimeTicks"`
	Album                 string            `json:"Album"`
	AlbumID               string            `json:"AlbumId"`
	AlbumPrimaryImageTag  string            `json:"AlbumPrimaryImageTag"`
	AlbumArtist           string            `json:"AlbumArtist"`
	Artists               []string          `json:"Artists"`
	AlbumArtists          []NameID          `json:"AlbumArtists"`
	Genres                []string          `json:"Genres"`
	Studios               []NameID          `json:"Studios"`
	OfficialRating        string            `json:"OfficialRating"`
	CommunityRating       float64           `json:"CommunityRating"`
	ProviderIDs           map[string]string `json:"ProviderIds"`
	ImageTags             map[string]string `json:"ImageTags"`
	ParentID              string            `json:"ParentId"`
	ChannelName           string            `json:"ChannelName"`
	ChannelID             string            `json:"ChannelId"`
	CollectionType        string            `json:"CollectionType"`
	IsFolder              bool              `json:"IsFolder"`
}

// Time is a timestamp that decodes leniently: unparseable or missing values become zero.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		t.Time = time.Time{}
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	t.Time = time.Time{}
	return nil
}

// PlayState is the session play state.
type PlayState struct {
	PositionTicks int64 `json:"PositionTicks"`
	IsPaused      bool  `json:"IsPaused"`
	IsMuted       bool  `json:"IsMuted"`
}

// Session is the subset of SessionInfoDto used for presence.
type Session struct {
	ID               string `json:"Id"`
	UserID           string `json:"UserId"`
	UserName         string `json:"UserName"`
	Client           string `json:"Client"`
	DeviceName       string `json:"DeviceName"`
	DeviceID         string `json:"DeviceId"`
	LastActivityDate Time   `json:"LastActivityDate"`
	// LastPlaybackCheckIn is when the client last reported PlayState.PositionTicks.
	LastPlaybackCheckIn Time      `json:"LastPlaybackCheckIn"`
	NowPlayingItem      *Item     `json:"NowPlayingItem"`
	PlayState           PlayState `json:"PlayState"`
}

// Sessions fetches recently active sessions. Callers filter by user locally because
// controllableByUserId would hide clients that don't support remote control.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	q := url.Values{"activeWithinSeconds": {"960"}}
	var out []Session
	if err := c.do(ctx, http.MethodGet, "/Sessions", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Ancestors returns the ancestors of an item (used to resolve library names).
func (c *Client) Ancestors(ctx context.Context, itemID, userID string) ([]Item, error) {
	q := url.Values{}
	if userID != "" {
		q.Set("userId", userID)
	}
	var out []Item
	if err := c.do(ctx, http.MethodGet, "/Items/"+url.PathEscape(itemID)+"/Ancestors", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ImageURL builds a public image URL for an item. base is the externally reachable server URL.
func ImageURL(base, itemID, imageType, tag string, maxHeight int) string {
	if base == "" || itemID == "" {
		return ""
	}
	q := url.Values{}
	if maxHeight > 0 {
		q.Set("maxHeight", fmt.Sprint(maxHeight))
	}
	q.Set("quality", "90")
	if tag != "" {
		q.Set("tag", tag)
	}
	return strings.TrimRight(base, "/") + "/Items/" + url.PathEscape(itemID) + "/Images/" + imageType + "?" + q.Encode()
}
