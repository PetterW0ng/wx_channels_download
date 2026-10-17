package api

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Every engine route, including adapter routes and WebSocket upgrades, is
// protected unless it is an explicit login or frontend bootstrap resource.
func (c *APIClient) require_login(ctx *gin.Context) {
	path := ctx.Request.URL.Path
	public := path == "/auth/login" || path == "/api/auth/login" || path == "/api/auth/session" || path == "/api/auth/logout"
	if ctx.Request.Method == http.MethodGet || ctx.Request.Method == http.MethodHead {
		public = public || path == "/" || path == "/favicon.ico" || strings.HasPrefix(path, "/__assets/public/") || strings.HasPrefix(path, "/__assets/src/")
	}
	if !public && !c.auth.Require(ctx.Writer, ctx.Request) {
		ctx.Abort()
		return
	}
	if strings.HasPrefix(path, "/ws/") {
		ctx.Writer = &authenticatedSocketWriter{ResponseWriter: ctx.Writer, request: ctx.Request, client: c}
	}
	ctx.Next()
}

// IssueInjectedSession is an optional capability consumed by the Channels interceptor.
func (c *APIClient) IssueInjectedSession(origin string) map[string]any {
	return c.auth.IssueInjectedSession(origin)
}

// Adapter-owned sockets must also close when the parent login is revoked.
type authenticatedSocketWriter struct {
	gin.ResponseWriter
	request *http.Request
	client  *APIClient
}

func (w *authenticatedSocketWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.Hijack()
	if err != nil {
		return nil, nil, err
	}
	release := w.client.auth.TrackConnection(w.request, conn)
	return &authenticatedConn{Conn: conn, release: release}, rw, nil
}

type authenticatedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *authenticatedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
