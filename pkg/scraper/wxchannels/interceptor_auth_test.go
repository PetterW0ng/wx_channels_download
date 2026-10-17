package wxchannels

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ltaoo/echo"
)

func TestInjectedAuthBootstrapIsLocalAndUncached(t *testing.T) {
	calls := 0
	plugins := NewInterceptorPlugins(InterceptorConfig{InjectedSession: func(origin string) map[string]any {
		calls++
		if origin != "https://channels.weixin.qq.com" {
			t.Error("wrong origin passed to grant issuer")
		}
		return map[string]any{"status": "success", "data": map[string]any{"token": "test-grant"}}
	}}, nil)
	ctx := &echo.Context{Req: httptest.NewRequest("GET", "https://channels.weixin.qq.com/__assets/auth/session", nil)}
	plugins[0].OnRequest(ctx)
	mock := ctx.GetMockResponse()
	if mock == nil || mock.StatusCode != 200 || mock.Headers["Cache-Control"] != "no-store" || calls != 1 {
		t.Fatal("bootstrap not intercepted")
	}
	if mock.Headers["Access-Control-Allow-Origin"] != "https://channels.weixin.qq.com" {
		t.Fatal("grant exposed to unrelated browser origins")
	}
	unrelated := &echo.Context{Req: httptest.NewRequest("GET", "https://untrusted.example/__assets/auth/session", nil)}
	plugins[0].OnRequest(unrelated)
	if calls != 1 {
		t.Fatal("grant issued for unrelated host")
	}
}

func TestInjectedAuthLoadsBeforeClientsWithoutEmbeddingCredentials(t *testing.T) {
	plugins := NewInterceptorPlugins(InterceptorConfig{InjectedSession: func(string) map[string]any {
		t.Error("HTML rendering must not mint/embed a credential")
		return nil
	}}, nil)
	ctx := &echo.Context{
		Req: httptest.NewRequest("GET", "https://channels.weixin.qq.com/web/pages/feed", nil),
		Res: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<html><head></head><body></body></html>"))},
	}
	plugins[0].OnResponse(ctx)
	body, err := ctx.GetResponseBody()
	if err != nil {
		t.Fatal(err)
	}
	a, b := strings.Index(body, "/inject/auth.js"), strings.Index(body, "/inject/utils.js")
	if a < 0 || b < a || !strings.Contains(body, `"injectedAuthRequired":true`) {
		t.Fatal("authentication bridge not loaded before HTTP/WS clients")
	}
}
