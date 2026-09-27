// Package client talks to the UniFi Network application's static DNS API on a
// UniFi OS gateway such as the Dream Router 7.
//
// The API has no endpoint for fetching a single record, so reads list every
// record. The list is cached briefly and shared between concurrent callers, and
// dropped after every write, so a Terraform plan or apply lists records once
// rather than once per resource.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"
)

// Record is one static DNS record as stored by the Network application.
// Numeric fields a record type doesn't use are stored as 0; a TTL of 0 means
// "automatic".
type Record struct {
	ID         string `json:"_id,omitempty"`
	RecordType string `json:"record_type"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Enabled    bool   `json:"enabled"`
	TTL        int64  `json:"ttl"`
	Priority   int64  `json:"priority"`
	Weight     int64  `json:"weight"`
	Port       int64  `json:"port"`
}

// APIError is an error response from the router.
type APIError struct {
	Method, Path string
	Status       int
	Code         string // e.g. "api.err.StaticDnsRecordAlreadyExists"
	Message      string
}

func (e *APIError) Error() string {
	msg := e.Message
	if e.Code != "" {
		msg = fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	if e.Status == http.StatusTooManyRequests {
		msg += "; the router limits logins per minute (success.login.limit.count in " +
			"/usr/lib/ulp-go/config.props), wait a minute and try again"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, msg)
}

// IsNotFound reports whether err is the router saying a record doesn't exist.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// IsAlreadyExists reports whether err is the router rejecting a duplicate record.
func IsAlreadyExists(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "api.err.StaticDnsRecordAlreadyExists"
}

// Config holds the connection settings.
type Config struct {
	Host               string // host or host:port, without scheme
	Site               string
	Username, Password string
	InsecureSkipVerify bool
	CacheTTL           time.Duration // how long a record list may be reused; 0 means DefaultCacheTTL
	HTTPClient         *http.Client  // optional, for tests

	// LoginRetryTimeout is how long to keep retrying a login that the router
	// refuses with HTTP 429 because its login limit has been reached. 0 means
	// fail immediately.
	LoginRetryTimeout time.Duration
	// Logf, if set, receives warnings such as "router login limit reached".
	Logf func(ctx context.Context, msg string, fields map[string]any)
}

// DefaultCacheTTL is long enough to cover the reads of one plan or apply.
const DefaultCacheTTL = 30 * time.Second

// Client is safe for concurrent use.
type Client struct {
	cfg    Config
	base   string
	dnsURL string
	http   *http.Client

	authMu   sync.Mutex // serialises logins and guards csrf/loggedIn
	csrf     string
	loggedIn bool

	writeMu sync.Mutex // serialises writes, so provisioning runs one change at a time

	cacheMu   sync.Mutex // held while fetching, so concurrent readers share one list call
	cache     []Record
	cacheTime time.Time

	retryIntervals []time.Duration // waits between rate-limited login attempts; the last one repeats
	loginWaited    time.Duration   // total time spent waiting for the login limit, guarded by authMu
}

// Waits between login attempts after HTTP 429. The router's limit is per
// minute, so the total wait rarely needs to exceed a minute.
var defaultRetryIntervals = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}

// New creates a client. It does not log in until the first request.
func New(cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("host is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("username and password are required")
	}
	if cfg.Site == "" {
		cfg.Site = "default"
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				// The gateway uses a self-signed certificate by default.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}, //nolint:gosec
			},
		}
	}
	if hc.Jar == nil {
		jar, _ := cookiejar.New(nil)
		clone := *hc
		clone.Jar = jar
		hc = &clone
	}
	base := cfg.Host
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	base = strings.TrimSuffix(base, "/")
	return &Client{
		cfg:            cfg,
		base:           base,
		dnsURL:         base + "/proxy/network/v2/api/site/" + cfg.Site + "/static-dns",
		http:           hc,
		retryIntervals: defaultRetryIntervals,
	}, nil
}

// LoginWaited returns how long the client has waited in total because the
// router's login limit was reached.
func (c *Client) LoginWaited() time.Duration {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.loginWaited
}

// List returns all static DNS records, using the cached list if it is fresh.
func (c *Client) List(ctx context.Context) ([]Record, error) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cache != nil && time.Since(c.cacheTime) < c.cfg.CacheTTL {
		return append([]Record(nil), c.cache...), nil
	}
	var records []Record
	if err := c.do(ctx, http.MethodGet, c.dnsURL, nil, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = []Record{}
	}
	c.cache, c.cacheTime = records, time.Now()
	return append([]Record(nil), records...), nil
}

// Get returns the record with the given ID, or an error satisfying IsNotFound.
func (c *Client) Get(ctx context.Context, id string) (Record, error) {
	records, err := c.List(ctx)
	if err != nil {
		return Record{}, err
	}
	for _, r := range records {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, &APIError{Method: http.MethodGet, Path: "static-dns/" + id, Status: http.StatusNotFound,
		Code: "api.err.StaticDnsRecordNotFound", Message: "Static DNS Record not found"}
}

// Create adds a record and returns it as stored, including its ID.
func (c *Client) Create(ctx context.Context, r Record) (Record, error) {
	r.ID = ""
	var out Record
	err := c.write(ctx, http.MethodPost, c.dnsURL, r, &out)
	return out, err
}

// Update replaces the record with r.ID and returns it as stored.
func (c *Client) Update(ctx context.Context, r Record) (Record, error) {
	if r.ID == "" {
		return Record{}, errors.New("update: record has no ID")
	}
	var out Record
	err := c.write(ctx, http.MethodPut, c.dnsURL+"/"+r.ID, r, &out)
	return out, err
}

// Delete removes the record with the given ID.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.write(ctx, http.MethodDelete, c.dnsURL+"/"+id, nil, nil)
}

func (c *Client) write(ctx context.Context, method, url string, body, out any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	defer c.invalidate()
	return c.do(ctx, method, url, body, out)
}

func (c *Client) invalidate() {
	c.cacheMu.Lock()
	c.cache = nil
	c.cacheMu.Unlock()
}

// do sends an authenticated request, logging in first if needed and once more
// if the session has expired.
func (c *Client) do(ctx context.Context, method, url string, body, out any) error {
	for attempt := 1; ; attempt++ {
		if err := c.ensureLogin(ctx); err != nil {
			return err
		}
		err := c.send(ctx, method, url, body, out)
		var apiErr *APIError
		if attempt == 1 && errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			c.authMu.Lock()
			c.loggedIn = false
			c.authMu.Unlock()
			continue
		}
		return err
	}
}

// ensureLogin logs in if there is no session. If the router refuses because
// its login limit has been reached (HTTP 429), it waits and retries until
// LoginRetryTimeout has passed. Other requests wait for it, as they need the
// session too.
func (c *Client) ensureLogin(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.loggedIn {
		return nil
	}
	body := map[string]any{"username": c.cfg.Username, "password": c.cfg.Password, "rememberMe": false}
	start := time.Now()
	deadline := start.Add(c.cfg.LoginRetryTimeout)
	for attempt := 0; ; attempt++ {
		err := c.sendLocked(ctx, http.MethodPost, c.base+"/api/auth/login", body, nil)
		if err == nil {
			c.loggedIn = true
			return nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
			return fmt.Errorf("logging in to %s as %q: %w", c.base, c.cfg.Username, err)
		}
		wait := c.retryIntervals[min(attempt, len(c.retryIntervals)-1)]
		if remaining := time.Until(deadline); remaining <= 0 {
			if c.cfg.LoginRetryTimeout > 0 {
				err = fmt.Errorf("still refused after retrying for %s: %w",
					time.Since(start).Round(time.Second), err)
			}
			return fmt.Errorf("logging in to %s as %q: %w", c.base, c.cfg.Username, err)
		} else if wait > remaining {
			wait = remaining
		}
		if c.cfg.Logf != nil {
			c.cfg.Logf(ctx, "router login limit reached, retrying", map[string]any{
				"host": c.base, "wait": wait.String(), "attempt": attempt + 1,
				"retry_timeout": c.cfg.LoginRetryTimeout.String(),
			})
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("logging in to %s: %w", c.base, ctx.Err())
		case <-time.After(wait):
		}
		c.loginWaited += wait
	}
}

func (c *Client) send(ctx context.Context, method, url string, body, out any) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.sendLocked(ctx, method, url, body, out)
}

// sendLocked performs one HTTP request. The caller holds authMu, which guards csrf.
func (c *Client) sendLocked(ctx context.Context, method, url string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if t := resp.Header.Get("X-Updated-CSRF-Token"); t != "" {
		c.csrf = t
	} else if t := resp.Header.Get("X-CSRF-Token"); t != "" {
		c.csrf = t
	}
	path := strings.TrimPrefix(url, c.base)
	if resp.StatusCode >= 400 {
		apiErr := &APIError{Method: method, Path: path, Status: resp.StatusCode}
		var payload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &payload) == nil && payload.Message != "" {
			apiErr.Code, apiErr.Message = payload.Code, payload.Message
		} else {
			apiErr.Message = http.StatusText(resp.StatusCode)
		}
		return apiErr
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: unexpected response: %w", method, path, err)
		}
	}
	return nil
}
