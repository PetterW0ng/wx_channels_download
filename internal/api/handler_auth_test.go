package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"wx_channel/internal/auth"
	"wx_channel/pkg/scraper/wxchannels"
)

func TestBusinessRoutesRequireLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &APIClient{engine: gin.New(), auth: auth.New("", 3)}
	c.engine.Use(c.require_login)
	c.SetupRoutes()
	c.RegisterGET("/api/adapter/test", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/config"}, {"POST", "/api/v1/download_task/create"},
		{"GET", "/api/file"}, {"GET", "/ws/v1/download_task"}, {"GET", "/ws/scraper"},
		{"GET", "/api/adapter/test"}, {"POST", "/mcp"}, {"GET", "/__assets/user/private.txt"},
	} {
		t.Run(route.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c.engine.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("unprotected route: %d", w.Code)
			}
		})
	}
	for _, path := range []string{"/auth/login", "/api/auth/login"} {
		if !shouldServeByAPI(path) {
			t.Errorf("login not routed to API: %s", path)
		}
		w := httptest.NewRecorder()
		c.engine.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 400 {
			t.Errorf("login incorrectly blocked: %d", w.Code)
		}
	}
}

func TestEmbeddedDownloadFlowWithoutManagementCookie(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".signature"
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message": "Alice", "data": token})
	}))
	defer upstream.Close()
	c := &APIClient{engine: gin.New(), auth: auth.New(upstream.URL, 3)}
	c.engine.Use(c.require_login)
	c.engine.POST("/api/auth/login", gin.WrapH(c.auth))
	c.engine.POST("/api/auth/logout", gin.WrapH(c.auth))
	for _, route := range []string{"/api/v1/download_task/create", "/api/browse_history/create"} {
		c.RegisterPOST(route, func(ctx *gin.Context) { ctx.JSON(200, gin.H{"code": 0}) })
	}
	channels := wxchannels.NewClient(wxchannels.ClientOptions{})
	c.RegisterGET("/ws/channels", func(ctx *gin.Context) {
		channels.ServeWebsocket(ctx.Writer, ctx.Request)
	})
	c.RegisterGET("/ws/v1/download_task", c.handle_download_task_ws)
	server := httptest.NewServer(c.engine)
	defer server.Close()
	resp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"identity":"alice","password":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	cookies := resp.Cookies()
	resp.Body.Close()
	if resp.StatusCode != 200 || len(cookies) != 1 {
		t.Fatal("management login failed")
	}
	grant := c.IssueInjectedSession("https://channels.weixin.qq.com")["data"].(map[string]any)["token"].(string)
	for _, route := range []string{"/api/v1/download_task/create", "/api/browse_history/create"} {
		req, _ := http.NewRequest("POST", server.URL+route, strings.NewReader(`{}`))
		req.Header.Set("Origin", "https://channels.weixin.qq.com")
		req.Header.Set("Authorization", "Bearer "+grant)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("embedded request %s failed: %d", route, response.StatusCode)
		}
	}
	var sockets []*websocket.Conn
	for _, route := range []string{"/ws/channels", "/ws/v1/download_task"} {
		dialer := websocket.Dialer{Subprotocols: []string{"wx-download.v1", "wx-auth." + grant}}
		conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+route, http.Header{"Origin": []string{"https://channels.weixin.qq.com"}})
		if err != nil {
			t.Fatalf("embedded websocket %s: %v", route, err)
		}
		defer conn.Close()
		// Browsers reject an empty negotiated protocol when protocols were offered.
		// Gorilla's client accepts it, so check explicitly to cover the webview.
		if conn.Subprotocol() != "wx-download.v1" {
			t.Fatalf("embedded websocket %s did not negotiate the application protocol", route)
		}
		sockets = append(sockets, conn)
	}
	// The management browser still connects using its cookie without protocols.
	cookieHeaders := http.Header{"Cookie": []string{cookies[0].String()}}
	management, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/v1/download_task", cookieHeaders)
	if err != nil {
		t.Fatalf("management websocket: %v", err)
	}
	defer management.Close()
	if management.Subprotocol() != "" {
		t.Fatal("server selected a protocol the management browser did not offer")
	}
	sockets = append(sockets, management)
	req, _ := http.NewRequest("POST", server.URL+"/api/auth/logout", nil)
	req.AddCookie(cookies[0])
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	for _, conn := range sockets {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err := conn.ReadMessage(); err == nil {
			t.Fatal("socket survived parent logout")
		}
	}
	req, _ = http.NewRequest("POST", server.URL+"/api/v1/download_task/create", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://channels.weixin.qq.com")
	req.Header.Set("Authorization", "Bearer "+grant)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("revoked embedded request accepted")
	}
}
