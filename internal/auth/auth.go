// Package auth exchanges CRM credentials for local, process-scoped sessions.
package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const CookieName = "wx_download_session"
const maxResponseSize = 1 << 20

type Session struct {
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Handler struct {
	signingKey    []byte
	latestSession [32]byte
	baseURL       string
	platformType  int
	client        *http.Client
	mu            sync.Mutex
	sessions      map[[32]byte]Session
	connections   map[[32]byte]map[io.Closer]struct{}
}

func New(baseURL string, platformType int) *Handler {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		key = nil
	}
	return &Handler{
		signingKey: key,
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"), platformType: platformType,
		client:      &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		sessions:    make(map[[32]byte]Session),
		connections: make(map[[32]byte]map[io.Closer]struct{}),
	}
}

func reply(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func failure(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"status": "error", "message": message, "code": status, "msg": message, "data": nil})
}

// SameOrigin also covers WebSocket handshakes. Non-browser API clients may omit Origin.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
}

func sessionKey(r *http.Request) string {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		for _, protocol := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
			if token, ok := strings.CutPrefix(strings.TrimSpace(protocol), "wx-auth."); ok {
				return token
			}
		}
	}
	if header := r.Header.Get("Authorization"); header != "" {
		if strings.HasPrefix(header, "Bearer ") {
			return strings.TrimPrefix(header, "Bearer ")
		}
		return ""
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (h *Handler) Session(r *http.Request) (Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, session, ok := h.resolveSessionLocked(r)
	return session, ok
}

func (h *Handler) resolveSessionLocked(r *http.Request) ([32]byte, Session, bool) {
	key := sessionKey(r)
	hash := sha256.Sum256([]byte(key))
	if strings.HasPrefix(key, "wxinject.") {
		parts := strings.Split(key, ".")
		if len(parts) != 3 || r.Header.Get("Origin") != "https://channels.weixin.qq.com" || !injectedPathAllowed(r) || len(h.signingKey) == 0 {
			return hash, Session{}, false
		}
		parent, err := hex.DecodeString(parts[1])
		signature, sigErr := base64.RawURLEncoding.DecodeString(parts[2])
		mac := hmac.New(sha256.New, h.signingKey)
		mac.Write([]byte(parts[0] + "." + parts[1]))
		if err != nil || sigErr != nil || len(parent) != len(hash) || !hmac.Equal(signature, mac.Sum(nil)) {
			return hash, Session{}, false
		}
		copy(hash[:], parent)
	} else if key == "" || !SameOrigin(r) {
		return hash, Session{}, false
	}
	session, ok := h.sessions[hash]
	if ok && !time.Now().Before(session.ExpiresAt) {
		h.revokeLocked(hash)
		return hash, Session{}, false
	}
	return hash, session, ok
}

// Injected pages get only the download/Channels capabilities, never settings,
// shell control, logout, or the primary browser's session cookie.
func injectedPathAllowed(r *http.Request) bool {
	p := r.URL.Path
	if r.Method == http.MethodGet {
		switch p {
		case "/api/auth/session", "/api/status", "/ws/channels", "/ws/v1/download_task", "/api/file", "/play", "/imgproxy":
			return true
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return false
	}
	if strings.HasPrefix(p, "/api/v1/download_task/") || strings.HasPrefix(p, "/api/channels/") {
		return true
	}
	return r.Method == http.MethodPost && (p == "/api/browse_history/create" || p == "/api/show_file" || p == "/api/v1/fs/list" || p == "/report")
}

// IssueInjectedSession is called only by the local Channels HTML interceptor.
// There is deliberately no public HTTP endpoint on the API to mint this grant.
func (h *Handler) IssueInjectedSession(origin string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[h.latestSession]
	if origin != "https://channels.weixin.qq.com" || !ok || !time.Now().Before(session.ExpiresAt) || len(h.signingKey) == 0 {
		return map[string]any{"status": "error", "message": "请先在下载器管理页面登录"}
	}
	body := "wxinject." + hex.EncodeToString(h.latestSession[:])
	mac := hmac.New(sha256.New, h.signingKey)
	mac.Write([]byte(body))
	token := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return map[string]any{"status": "success", "data": map[string]any{"token": token, "expires_at": session.ExpiresAt}}
}

func (h *Handler) Require(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := h.Session(r); ok {
		return true
	}
	failure(w, http.StatusUnauthorized, "请先登录，或重新登录已过期的会话")
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, validSession := h.Session(r)
	delegatedSessionRead := r.Method == http.MethodGet && r.URL.Path == "/api/auth/session" && validSession
	if !SameOrigin(r) && !delegatedSessionRead {
		failure(w, http.StatusForbidden, "不允许跨站认证请求")
		return
	}
	switch {
	case r.Method == http.MethodPost && (r.URL.Path == "/api/auth/login" || r.URL.Path == "/auth/login"):
		h.login(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/auth/session":
		session, ok := h.Session(r)
		if !ok {
			failure(w, http.StatusUnauthorized, "请先登录")
			return
		}
		reply(w, http.StatusOK, map[string]any{"status": "success", "data": session})
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/logout":
		h.mu.Lock()
		h.revokeLocked(sha256.Sum256([]byte(sessionKey(r))))
		h.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		reply(w, http.StatusOK, map[string]any{"status": "success", "data": nil})
	default:
		failure(w, http.StatusNotFound, "接口不存在")
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Identity     string `json:"identity"`
		Password     string `json:"password"`
		PlatformType *int   `json:"platformType,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&input); err != nil {
		failure(w, 400, "登录参数格式错误")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		failure(w, 400, "登录参数格式错误")
		return
	}
	input.Identity = strings.TrimSpace(input.Identity)
	if input.Identity == "" || input.Password == "" {
		failure(w, 400, "请输入账号和密码")
		return
	}
	// The deployment selects its CRM platform; callers cannot switch account namespaces.
	if input.PlatformType != nil && *input.PlatformType != h.platformType {
		failure(w, 400, "platformType 与服务配置不一致")
		return
	}
	input.PlatformType = &h.platformType
	u, err := url.Parse(h.baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		failure(w, 503, "登录服务地址未正确配置")
		return
	}
	body, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.baseURL+"/auth/login", bytes.NewReader(body))
	if err != nil {
		failure(w, 503, "登录服务地址未正确配置")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		failure(w, 502, "无法连接登录服务，请稍后重试")
		return
	}
	defer resp.Body.Close()
	var payload struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		failure(w, 401, "账号或密码错误")
		return
	}
	if resp.StatusCode == 429 {
		failure(w, 429, "登录过于频繁，请稍后重试")
		return
	}
	if err != nil || len(raw) > maxResponseSize || json.Unmarshal(raw, &payload) != nil || resp.StatusCode != 200 {
		failure(w, 502, "登录服务返回异常，请稍后重试")
		return
	}
	if payload.Status != "success" || strings.TrimSpace(payload.Data) == "" {
		failure(w, 401, "登录失败，请检查账号和密码")
		return
	}
	// Only read expiry from a token received directly from the trusted CRM response.
	// This is never used to validate a token supplied by a client.
	expires := time.Now().Add(72 * time.Hour)
	parts := strings.Split(payload.Data, ".")
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if len(parts) != 3 {
		failure(w, 502, "登录服务未返回有效 Token")
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(decoded, &claims) != nil || claims.Exp <= time.Now().Unix() {
		failure(w, 502, "登录服务返回的 Token 无效或已过期")
		return
	}
	if tokenExpiry := time.Unix(claims.Exp, 0); tokenExpiry.Before(expires) {
		expires = tokenExpiry
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		failure(w, 500, "无法创建登录会话")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	session := Session{Username: payload.Message, ExpiresAt: expires}
	if session.Username == "" {
		session.Username = input.Identity
	}
	h.mu.Lock()
	for key, s := range h.sessions {
		if !time.Now().Before(s.ExpiresAt) {
			h.revokeLocked(key)
		}
	}
	// Re-login replaces this browser's previous session.
	h.revokeLocked(sha256.Sum256([]byte(sessionKey(r))))
	h.latestSession = sha256.Sum256([]byte(token))
	h.sessions[h.latestSession] = session
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	// Preserve CRM's response shape and token for callers of other CRM APIs.
	// The CRM token itself is deliberately not accepted as a local session.
	reply(w, 200, map[string]any{"status": "success", "message": session.Username, "data": payload.Data, "expires_at": expires})
}

// TrackConnection closes a WebSocket on logout or session expiry. The caller
// releases tracking when its read loop ends.
func (h *Handler) TrackConnection(r *http.Request, conn io.Closer) func() {
	h.mu.Lock()
	hash, session, ok := h.resolveSessionLocked(r)
	if !ok || !time.Now().Before(session.ExpiresAt) {
		h.mu.Unlock()
		_ = conn.Close()
		return func() {}
	}
	if h.connections[hash] == nil {
		h.connections[hash] = make(map[io.Closer]struct{})
	}
	h.connections[hash][conn] = struct{}{}
	h.mu.Unlock()
	timer := time.AfterFunc(time.Until(session.ExpiresAt), func() {
		h.mu.Lock()
		h.revokeLocked(hash)
		h.mu.Unlock()
	})
	return func() {
		timer.Stop()
		h.mu.Lock()
		delete(h.connections[hash], conn)
		if len(h.connections[hash]) == 0 {
			delete(h.connections, hash)
		}
		h.mu.Unlock()
	}
}

func (h *Handler) revokeLocked(hash [32]byte) {
	delete(h.sessions, hash)
	for conn := range h.connections[hash] {
		_ = conn.Close()
	}
	delete(h.connections, hash)
}
