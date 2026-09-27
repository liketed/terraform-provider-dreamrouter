package client

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liketed/terraform-provider-dreamrouter/internal/fakerouter"
)

func newTestClient(t *testing.T, r *fakerouter.Router, cacheTTL time.Duration) *Client {
	t.Helper()
	c, err := New(Config{Host: r.Host(), Username: fakerouter.Username, Password: fakerouter.Password,
		InsecureSkipVerify: true, CacheTTL: cacheTTL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCRUD(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	c := newTestClient(t, r, time.Minute)
	ctx := context.Background()

	created, err := c.Create(ctx, Record{RecordType: "SRV", Key: "_sip._tcp.home.internal",
		Value: "pbx.home.internal", Enabled: true, Priority: 10, Weight: 5, Port: 5060})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Port != 5060 {
		t.Fatalf("unexpected created record %+v", created)
	}

	got, err := c.Get(ctx, created.ID)
	if err != nil || got != created {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, created)
	}

	got.Port = 5061
	updated, err := c.Update(ctx, got)
	if err != nil || updated.Port != 5061 {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	if again, _ := c.Get(ctx, created.ID); again.Port != 5061 {
		t.Fatalf("Get after update returned stale record %+v", again)
	}

	if err := c.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, created.ID); !IsNotFound(err) {
		t.Fatalf("Get after delete: err = %v, want not found", err)
	}
	if err := c.Delete(ctx, created.ID); !IsNotFound(err) {
		t.Fatalf("second Delete: err = %v, want not found", err)
	}
	if _, err := c.Update(ctx, got); !IsNotFound(err) {
		t.Fatalf("Update of deleted record: err = %v, want not found", err)
	}
}

func TestErrors(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	c := newTestClient(t, r, time.Minute)
	ctx := context.Background()

	a := Record{RecordType: "A", Key: "nas.home.internal", Value: "192.168.1.50", Enabled: true}
	if _, err := c.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, a); !IsAlreadyExists(err) {
		t.Fatalf("duplicate: err = %v, want already exists", err)
	}
	_, err := c.Create(ctx, Record{RecordType: "A", Key: "x.home.internal", Value: "999.1.1.1"})
	if err == nil || !strings.Contains(err.Error(), "Invalid IPv4 Address") {
		t.Fatalf("bad IP: err = %v", err)
	}

	bad, _ := New(Config{Host: r.Host(), Username: "admin", Password: "wrong", InsecureSkipVerify: true})
	if _, err := bad.List(ctx); err == nil || !strings.Contains(err.Error(), "Invalid username or password") {
		t.Fatalf("wrong password: err = %v", err)
	}
}

func TestLoginOnceAndCache(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	c := newTestClient(t, r, time.Minute)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.List(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if r.Logins != 1 || r.ListCalls != 1 {
		t.Fatalf("20 concurrent reads: logins=%d listCalls=%d, want 1 and 1", r.Logins, r.ListCalls)
	}

	if _, err := c.Create(ctx, Record{RecordType: "TXT", Key: "t.home.internal", Value: "hello"}); err != nil {
		t.Fatal(err)
	}
	records, _ := c.List(ctx)
	if len(records) != 1 || r.ListCalls != 2 {
		t.Fatalf("after write: %d records, listCalls=%d; want cache refreshed", len(records), r.ListCalls)
	}
	if r.Logins != 1 {
		t.Fatalf("logins = %d, want 1", r.Logins)
	}
}

func TestCacheExpires(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	c := newTestClient(t, r, 50*time.Millisecond)
	ctx := context.Background()
	_, _ = c.List(ctx)
	r.Put(fakerouter.Record{RecordType: "A", Key: "ui.home.internal", Value: "192.168.1.9", Enabled: true})
	time.Sleep(80 * time.Millisecond)
	records, _ := c.List(ctx)
	if len(records) != 1 {
		t.Fatalf("expired cache not refreshed: %d records", len(records))
	}
}

func TestRelogin(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	c := newTestClient(t, r, time.Nanosecond)
	ctx := context.Background()
	if _, err := c.List(ctx); err != nil {
		t.Fatal(err)
	}
	r.ExpireSessions()
	if _, err := c.List(ctx); err != nil {
		t.Fatalf("after session expiry: %v", err)
	}
	if r.Logins != 2 {
		t.Fatalf("logins = %d, want 2", r.Logins)
	}
}

func TestRateLimitMessage(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.LoginLimit = 1
	ctx := context.Background()
	_, _ = newTestClient(t, r, time.Minute).List(ctx)
	_, err := newTestClient(t, r, time.Minute).List(ctx)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "success.login.limit.count") {
		t.Fatalf("err = %v, want 429 with rate-limit hint", err)
	}
}

// logRecorder collects Logf calls.
type logRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logRecorder) logf(_ context.Context, msg string, _ map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
}

func (l *logRecorder) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.msgs)
}

func newRetryClient(t *testing.T, r *fakerouter.Router, timeout time.Duration, logs *logRecorder) *Client {
	t.Helper()
	c, err := New(Config{Host: r.Host(), Username: fakerouter.Username, Password: fakerouter.Password,
		InsecureSkipVerify: true, LoginRetryTimeout: timeout, Logf: logs.logf})
	if err != nil {
		t.Fatal(err)
	}
	c.retryIntervals = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	return c
}

func TestLoginRetrySucceedsWhenLimitResets(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.SetLoginLimit(1)
	ctx := context.Background()
	if _, err := newTestClient(t, r, time.Minute).List(ctx); err != nil { // uses up the limit
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		r.SetLoginLimit(0) // the router's per-minute window resets
	}()
	logs := &logRecorder{}
	c := newRetryClient(t, r, 5*time.Second, logs)
	if _, err := c.List(ctx); err != nil {
		t.Fatalf("List after limit reset: %v", err)
	}
	if logs.count() == 0 || logs.msgs[0] != "router login limit reached, retrying" {
		t.Fatalf("log messages = %q", logs.msgs)
	}
	if c.LoginWaited() < 100*time.Millisecond {
		t.Fatalf("LoginWaited = %s, want the time spent waiting", c.LoginWaited())
	}
	if r.LoginCount() != 2 {
		t.Fatalf("successful logins = %d, want 2", r.LoginCount())
	}
}

func TestLoginRetryGivesUp(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.SetLoginLimit(1)
	ctx := context.Background()
	_, _ = newTestClient(t, r, time.Minute).List(ctx)
	logs := &logRecorder{}
	start := time.Now()
	_, err := newRetryClient(t, r, 200*time.Millisecond, logs).List(ctx)
	if err == nil || !strings.Contains(err.Error(), "still refused after retrying") || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("gave up after %s, want about the 200ms timeout", elapsed)
	}
	if logs.count() < 2 {
		t.Fatalf("logged %d retries, want several", logs.count())
	}
}

func TestLoginNoRetryWhenDisabled(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.SetLoginLimit(1)
	ctx := context.Background()
	_, _ = newTestClient(t, r, time.Minute).List(ctx)
	logs := &logRecorder{}
	start := time.Now()
	_, err := newRetryClient(t, r, 0, logs).List(ctx)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || strings.Contains(err.Error(), "retrying") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second || logs.count() != 0 {
		t.Fatalf("retried although disabled: %s, %d logs", time.Since(start), logs.count())
	}
}

func TestLoginRetryStopsOnCancel(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.SetLoginLimit(1)
	_, _ = newTestClient(t, r, time.Minute).List(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newRetryClient(t, r, time.Minute, &logRecorder{}).List(ctx)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %s, want prompt cancellation", err, time.Since(start))
	}
}

func TestLoginWrongPasswordIsNotRetried(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	logs := &logRecorder{}
	c, _ := New(Config{Host: r.Host(), Username: "admin", Password: "wrong", InsecureSkipVerify: true,
		LoginRetryTimeout: time.Minute, Logf: logs.logf})
	start := time.Now()
	if _, err := c.List(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid username or password") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second || logs.count() != 0 {
		t.Fatal("a wrong password was retried")
	}
}
