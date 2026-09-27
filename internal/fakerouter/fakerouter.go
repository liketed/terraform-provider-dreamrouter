// Package fakerouter is an in-memory stand-in for the UniFi OS login and static
// DNS API, for tests. It mimics the behaviour observed on a real Dream Router 7:
// cookie + CSRF sessions, full records returned from POST/PUT, 404 for unknown
// IDs, 405 for GET by ID, and the same error codes for duplicates and CNAME
// overlaps.
package fakerouter

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

const (
	Username = "admin"
	Password = "secret"
	Site     = "default"
)

type Record struct {
	ID         string `json:"_id"`
	RecordType string `json:"record_type"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Enabled    bool   `json:"enabled"`
	TTL        int64  `json:"ttl"`
	Priority   int64  `json:"priority"`
	Weight     int64  `json:"weight"`
	Port       int64  `json:"port"`
}

type Router struct {
	*httptest.Server

	mu         sync.Mutex
	records    []Record
	nextID     int
	sessions   map[string]bool
	csrf       string
	Logins     int // successful logins
	ListCalls  int
	LoginLimit int // successful logins allowed before returning 429; 0 = unlimited
}

// New starts a TLS test server. Use Host() for the provider's host setting.
func New() *Router {
	r := &Router{sessions: map[string]bool{}, csrf: "csrf-token-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", r.login)
	prefix := "/proxy/network/v2/api/site/" + Site + "/static-dns"
	mux.HandleFunc(prefix, r.collection)
	mux.HandleFunc(prefix+"/", r.item)
	r.Server = httptest.NewTLSServer(mux)
	return r
}

// Host returns host:port of the server.
func (r *Router) Host() string { return strings.TrimPrefix(r.URL, "https://") }

// Records returns a copy of the stored records.
func (r *Router) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record(nil), r.records...)
}

// Put stores a record directly, bypassing validation, and returns its ID.
func (r *Router) Put(rec Record) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	rec.ID = fmt.Sprintf("%024x", r.nextID)
	r.records = append(r.records, rec)
	return rec.ID
}

// Remove deletes a record directly, as if someone deleted it in the web UI.
func (r *Router) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, rec := range r.records {
		if rec.ID == id {
			r.records = append(r.records[:i], r.records[i+1:]...)
			return
		}
	}
}

// Modify changes a stored record directly, as if someone edited it in the web UI.
func (r *Router) Modify(id string, fn func(*Record)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.records {
		if r.records[i].ID == id {
			fn(&r.records[i])
		}
	}
}

// SetLoginLimit changes how many successful logins are allowed in total
// (0 = unlimited), e.g. to simulate the router's limit resetting.
func (r *Router) SetLoginLimit(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LoginLimit = n
}

// LoginCount returns the number of successful logins so far.
func (r *Router) LoginCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Logins
}

// ExpireSessions logs every client out, as if the session timed out.
func (r *Router) ExpireSessions() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = map[string]bool{}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "errorCode": status})
}

func (r *Router) login(w http.ResponseWriter, req *http.Request) {
	var body struct{ Username, Password string }
	if req.Method != http.MethodPost || json.NewDecoder(req.Body).Decode(&body) != nil {
		writeErr(w, http.StatusBadRequest, "", "bad request")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if body.Username != Username || body.Password != Password {
		writeErr(w, http.StatusForbidden, "AUTHENTICATION_FAILED_INVALID_CREDENTIALS", "Invalid username or password")
		return
	}
	if r.LoginLimit > 0 && r.Logins >= r.LoginLimit {
		writeErr(w, http.StatusTooManyRequests, "AUTHENTICATION_FAILED_LIMIT_REACHED", "You've reached the login attempt limit")
		return
	}
	r.Logins++
	token := fmt.Sprintf("session-%d", r.Logins)
	r.sessions[token] = true
	http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: token, Path: "/"})
	w.Header().Set("X-CSRF-Token", r.csrf)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"username":"admin"}`))
}

// authorised checks the session cookie, and the CSRF token on writes.
func (r *Router) authorised(w http.ResponseWriter, req *http.Request) bool {
	c, err := req.Cookie("TOKEN")
	if err != nil || !r.sessions[c.Value] {
		writeErr(w, http.StatusUnauthorized, "api.err.UserNotAuthenticated", "User not authenticated")
		return false
	}
	if req.Method != http.MethodGet && req.Header.Get("X-CSRF-Token") != r.csrf {
		writeErr(w, http.StatusForbidden, "api.err.InvalidCsrf", "Invalid CSRF token")
		return false
	}
	return true
}

func (r *Router) collection(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.authorised(w, req) {
		return
	}
	switch req.Method {
	case http.MethodGet:
		r.ListCalls++
		writeJSON(w, r.records)
	case http.MethodPost:
		var rec Record
		if json.NewDecoder(req.Body).Decode(&rec) != nil {
			writeErr(w, http.StatusBadRequest, "", "bad request")
			return
		}
		if !r.validate(w, rec, "") {
			return
		}
		r.nextID++
		rec.ID = fmt.Sprintf("%024x", r.nextID)
		r.records = append(r.records, rec)
		writeJSON(w, rec)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "", "Method Not Allowed")
	}
}

func (r *Router) item(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.authorised(w, req) {
		return
	}
	id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
	idx := -1
	for i, rec := range r.records {
		if rec.ID == id {
			idx = i
		}
	}
	switch req.Method {
	case http.MethodPut:
		var rec Record
		if json.NewDecoder(req.Body).Decode(&rec) != nil {
			writeErr(w, http.StatusBadRequest, "", "bad request")
			return
		}
		if idx < 0 {
			writeErr(w, http.StatusNotFound, "api.err.StaticDnsRecordNotFound", "Static DNS Record not found")
			return
		}
		if !r.validate(w, rec, id) {
			return
		}
		rec.ID = id
		r.records[idx] = rec
		writeJSON(w, rec)
	case http.MethodDelete:
		if idx < 0 {
			writeErr(w, http.StatusNotFound, "api.err.StaticDnsRecordNotFound", "Static DNS Record not found")
			return
		}
		r.records = append(r.records[:idx], r.records[idx+1:]...)
		w.WriteHeader(http.StatusOK)
	default: // the real router has no GET by ID
		writeErr(w, http.StatusMethodNotAllowed, "", "Method Not Allowed")
	}
}

// validate applies the checks the real router makes that matter for tests.
func (r *Router) validate(w http.ResponseWriter, rec Record, selfID string) bool {
	invalid := func(msg string) bool {
		writeErr(w, http.StatusBadRequest, "api.err.StaticDnsRecordInvalidParameters", msg)
		return false
	}
	switch rec.RecordType {
	case "A":
		if ip := net.ParseIP(rec.Value); ip == nil || ip.To4() == nil {
			return invalid("Invalid IPv4 Address")
		}
	case "AAAA":
		if ip := net.ParseIP(rec.Value); ip == nil || ip.To4() != nil {
			return invalid("Invalid IPv6 Address")
		}
	case "NS":
		if net.ParseIP(rec.Value) == nil {
			return invalid("Target DNS server address must be a valid IPv4 or IPv6 Address")
		}
	case "CNAME":
		if rec.Key == rec.Value {
			return invalid("Invalid CNAME record! Domain name cannot be the same as alias name.")
		}
	case "MX", "SRV", "TXT":
	default:
		return invalid("Invalid record type")
	}
	for _, o := range r.records {
		if o.ID == selfID || o.Key != rec.Key {
			continue
		}
		if o.RecordType == rec.RecordType && o.Value == rec.Value {
			writeErr(w, http.StatusBadRequest, "api.err.StaticDnsRecordAlreadyExists", "Static DNS Record Already Exists")
			return false
		}
		if o.RecordType == "CNAME" || rec.RecordType == "CNAME" {
			writeErr(w, http.StatusBadRequest, "api.err.StaticDnsCnameAliasOverlapsWithOtherRecords", "CNAME Alias Overlaps With Other Records")
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
