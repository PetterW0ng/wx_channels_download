package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testToken(expiry time.Time) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix()))) + ".signature"
}

func request(h *Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLoginLifecycle(t *testing.T) {
	token := testToken(time.Now().Add(time.Hour))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" || r.Method != "POST" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var input map[string]any
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input["identity"] != "alice" || input["password"] != " password " || input["platformType"] != float64(3) {
			t.Errorf("unexpected credentials: fields do not match")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message": "Alice", "data": token})
	}))
	defer upstream.Close()
	h := New(upstream.URL+"/api", 3)
	w := request(h, "POST", "/auth/login", `{"identity":" alice ","password":" password "}`, nil)
	if w.Code != 200 {
		t.Fatalf("login failed: %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Value == token {
		t.Fatal("session cookie is missing or unsafe")
	}
	cookie := cookies[0]
	if got := request(h, "GET", "/api/auth/session", "", cookie); got.Code != 200 || !strings.Contains(got.Body.String(), "Alice") {
		t.Fatal("session not restored")
	}
	forged := &http.Cookie{Name: CookieName, Value: token}
	if got := request(h, "GET", "/api/auth/session", "", forged); got.Code != 401 {
		t.Fatal("accepted external token as local session")
	}
	r := httptest.NewRequest("GET", "/ws/scraper", nil)
	r.AddCookie(cookie)
	connection := &testConnection{}
	release := h.TrackConnection(r, connection)
	defer release()
	if got := request(h, "POST", "/api/auth/logout", "", cookie); got.Code != 200 || got.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed")
	}
	if connection.closed.Load() != 1 {
		t.Fatal("logout did not close WebSocket")
	}
	if got := request(h, "GET", "/api/auth/session", "", cookie); got.Code != 401 {
		t.Fatal("logged-out session accepted")
	}
}

type testConnection struct{ closed atomic.Int32 }

func (c *testConnection) Close() error { c.closed.Add(1); return nil }

func TestRejectedLogins(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"wrong password", 401, `{"status":"error"}`, 401},
		{"rate limited", 429, `too many requests`, 429},
		{"upstream failure", 500, `internal error with secrets`, 502},
		{"malformed response", 200, `not json`, 502},
		{"missing token", 200, `{"status":"success","data":""}`, 401},
		{"business error", 200, `{"status":"error","data":"abc"}`, 401},
		{"expired token", 200, fmt.Sprintf(`{"status":"success","data":%q}`, testToken(time.Now().Add(-time.Hour))), 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			h := New(upstream.URL, 3)
			w := request(h, "POST", "/api/auth/login", `{"identity":"alice","password":"bad"}`, nil)
			if w.Code != tc.want || len(w.Result().Cookies()) != 0 || len(h.sessions) != 0 {
				t.Fatalf("unexpected auth result %d", w.Code)
			}
			if strings.Contains(w.Body.String(), "secrets") {
				t.Fatal("leaked upstream response")
			}
		})
	}
}

func TestValidationAndCrossOrigin(t *testing.T) {
	h := New("http://127.0.0.1:1/api", 3)
	for _, body := range []string{`{`, `{}`, `{"identity":" ","password":"p"}`, `{"identity":"alice","password":"p","platformType":4}`, `{"identity":"alice","password":"p"} {}`} {
		if w := request(h, "POST", "/auth/login", body, nil); w.Code != 400 {
			t.Errorf("expected 400, got %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/api/auth/login", strings.NewReader(`{"identity":"alice","password":"p"}`))
	r.Header.Set("Origin", "https://untrusted.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin login allowed")
	}
	if w := request(h, "POST", "/auth/login", `{"identity":"alice","password":"p"}`, nil); w.Code != 502 {
		t.Fatal("network failure not handled")
	}
	h.baseURL = "ftp://example.com"
	if w := request(h, "POST", "/auth/login", `{"identity":"alice","password":"p"}`, nil); w.Code != 503 {
		t.Fatal("invalid base URL accepted")
	}
}

func TestExpiryAndRequire(t *testing.T) {
	h := New("", 3)
	hash := sha256.Sum256([]byte("session"))
	h.sessions[hash] = Session{Username: "Alice", ExpiresAt: time.Now().Add(50 * time.Millisecond)}
	r := httptest.NewRequest("GET", "/ws/scraper", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "session"})
	c := &testConnection{}
	release := h.TrackConnection(r, c)
	defer release()
	if !h.Require(httptest.NewRecorder(), r) {
		t.Fatal("active session rejected")
	}
	deadline := time.Now().Add(time.Second)
	for c.closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.closed.Load() == 0 {
		t.Fatal("expired WebSocket not closed")
	}
	w := httptest.NewRecorder()
	if h.Require(w, r) || w.Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	w := request(New(source.URL, 3), "POST", "/auth/login", `{"identity":"alice","password":"p"}`, nil)
	if w.Code != 502 || received.Load() != 0 {
		t.Fatal("followed authentication redirect")
	}
}

func TestInjectedSessionIsBoundToParentOriginAndScope(t *testing.T) {
	h := New("", 3)
	origin := "https://channels.weixin.qq.com"
	if h.IssueInjectedSession(origin)["status"] != "error" {
		t.Fatal("issued a grant before login")
	}
	parentToken := "local-browser-session"
	h.latestSession = sha256.Sum256([]byte(parentToken))
	h.sessions[h.latestSession] = Session{Username: "Alice", ExpiresAt: time.Now().Add(time.Hour)}
	if h.IssueInjectedSession("https://untrusted.example")["status"] != "error" {
		t.Fatal("issued grant for unrelated origin")
	}
	grant := h.IssueInjectedSession(origin)["data"].(map[string]any)["token"].(string)
	for _, route := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/ws/channels", true}, {"GET", "/ws/v1/download_task", true},
		{"POST", "/api/v1/download_task/create", true}, {"POST", "/api/browse_history/create", true},
		{"GET", "/api/auth/session", true}, {"POST", "/api/config", false},
		{"POST", "/api/auth/logout", false}, {"POST", "/api/auth/login", false},
		{"GET", "/api/logs", false}, {"POST", "/api/service/stop", false},
	} {
		r := httptest.NewRequest(route.method, route.path, nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+grant)
		if _, ok := h.Session(r); ok != route.allowed {
			t.Errorf("%s %s authorization=%v", route.method, route.path, ok)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/download_task/create", nil)
	r.Header.Set("Authorization", "Bearer "+grant)
	for _, wrongOrigin := range []string{"", "https://untrusted.example", "http://channels.weixin.qq.com"} {
		r.Header.Set("Origin", wrongOrigin)
		if _, ok := h.Session(r); ok {
			t.Fatal("accepted incorrect origin")
		}
	}
	r.Header.Set("Origin", origin)
	r.Header.Set("Authorization", "Bearer "+grant+"tampered")
	if _, ok := h.Session(r); ok {
		t.Fatal("accepted tampered grant")
	}
	r.Header.Set("Authorization", "Bearer "+grant)
	conn := &testConnection{}
	release := h.TrackConnection(r, conn)
	defer release()
	request(h, "POST", "/api/auth/logout", "", &http.Cookie{Name: CookieName, Value: parentToken})
	if _, ok := h.Session(r); ok {
		t.Fatal("parent logout did not revoke grant")
	}
	if conn.closed.Load() != 1 {
		t.Fatal("parent logout did not close delegated connection")
	}
	if h.IssueInjectedSession(origin)["status"] != "error" {
		t.Fatal("issued grant after logout")
	}
}

func TestInjectedWebSocketProtocolAndSessionRead(t *testing.T) {
	h := New("", 3)
	h.latestSession = sha256.Sum256([]byte("parent"))
	h.sessions[h.latestSession] = Session{Username: "Alice", ExpiresAt: time.Now().Add(time.Hour)}
	grant := h.IssueInjectedSession("https://channels.weixin.qq.com")["data"].(map[string]any)["token"].(string)
	r := httptest.NewRequest("GET", "/ws/channels", nil)
	r.Header.Set("Origin", "https://channels.weixin.qq.com")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Protocol", "wx-auth."+grant)
	if _, ok := h.Session(r); !ok {
		t.Fatal("WebSocket protocol credential rejected")
	}
	r = httptest.NewRequest("GET", "/api/auth/session", nil)
	r.Header.Set("Origin", "https://channels.weixin.qq.com")
	r.Header.Set("Authorization", "Bearer "+grant)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("embedded session check rejected: %d", w.Code)
	}
	h.sessions[h.latestSession] = Session{ExpiresAt: time.Now().Add(-time.Second)}
	if _, ok := h.Session(r); ok {
		t.Fatal("accepted expired parent")
	}
}
