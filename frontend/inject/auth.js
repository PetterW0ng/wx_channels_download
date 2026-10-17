// Bridge the management login into the separate WeChat webview. Grants are
// minted by our local interceptor and sent only to our configured API origin.
(function () {
  const config = window.__d_config || {};
  if (!config.injectedAuthRequired || window.WXAuth) return;
  const nativeFetch = window.fetch.bind(window);
  const NativeSocket = window.WebSocket;
  const apiOrigin = new URL(config.apiOrigin).origin;
  const sockets = new Set();
  const requests = new Set();
  const xhrRequests = new Set();
  let token = "";
  let expires = 0;
  let timer;
  let stopped = false;
  let ready = false;
  let refreshing = null;
  const authError = () => Object.assign(new Error("请先在下载器管理页面登录，再重试下载"), { status: 401 });
  function isAPI(input) {
    const url = new URL(typeof input === "string" || input instanceof URL ? input : input.url, window.location.href);
    const httpOrigin = url.origin.replace(/^ws/, "http");
    return httpOrigin === apiOrigin && (/^\/(api|ws)\//.test(url.pathname) || ["/report", "/play", "/imgproxy"].includes(url.pathname));
  }
  function active() { return !!token && Date.now() < expires; }
  function clear() {
    const wasReady = ready;
    token = ""; expires = 0; ready = false;
    for (const socket of sockets) socket.close(1000, "login session ended");
    for (const controller of requests) controller.abort();
    for (const xhr of xhrRequests) xhr.abort();
    if (wasReady) window.dispatchEvent(new Event("wx-auth-required"));
  }
  function requireAuth() { if (!active()) { clear(); throw authError(); } }
  async function refreshSession() {
    if (stopped) return;
    const abort = new AbortController();
    const timeout = setTimeout(() => abort.abort(), 10000);
    try {
      // This same-origin path is mocked by the app's Channels interceptor, not
      // the local API. It never receives the primary session cookie/password.
      const response = await nativeFetch("/__assets/auth/session", { cache: "no-store", credentials: "omit", signal: abort.signal });
      const payload = await response.json();
      if (stopped) return;
      if (payload.status !== "success" || !payload.data?.token) { clear(); return; }
      if (token && token !== payload.data.token) clear();
      token = payload.data.token;
      expires = Date.parse(payload.data.expires_at);
      if (!active()) { clear(); return; }
      if (!ready) {
        ready = true;
        window.dispatchEvent(new Event("wx-auth-ready"));
      }
    } catch {
      if (!active()) clear();
    } finally {
      clearTimeout(timeout);
    }
  }
  function refresh() {
    if (refreshing) return refreshing;
    clearTimeout(timer);
    refreshing = refreshSession().finally(() => {
      refreshing = null;
      if (!stopped) timer = setTimeout(refresh, active() ? 15000 : 3000);
    });
    return refreshing;
  }
  window.WXAuth = { active, refresh };
  window.fetch = async function (input, options = {}) {
    if (!isAPI(input)) return nativeFetch(input, options);
    requireAuth();
    const headers = new Headers(options.headers || input.headers);
    headers.set("Authorization", "Bearer " + token);
    const controller = new AbortController();
    const signal = options.signal || input.signal;
    const abort = () => controller.abort();
    if (signal?.aborted) abort();
    signal?.addEventListener("abort", abort, { once: true });
    requests.add(controller);
    try {
      const response = await nativeFetch(input, { ...options, headers, credentials: "omit", signal: controller.signal });
      if (response.status === 401) clear();
      return response;
    } finally {
      requests.delete(controller);
      signal?.removeEventListener("abort", abort);
    }
  };
  const xhrMeta = new WeakMap();
  const xhrProto = window.XMLHttpRequest.prototype;
  const originalOpen = xhrProto.open;
  const originalSend = xhrProto.send;
  xhrProto.open = function (method, url, ...rest) {
    xhrMeta.set(this, isAPI(url));
    return originalOpen.call(this, method, url, ...rest);
  };
  xhrProto.send = function (...args) {
    if (xhrMeta.get(this)) {
      requireAuth();
      this.setRequestHeader("Authorization", "Bearer " + token);
      xhrRequests.add(this);
      this.addEventListener("loadend", () => {
        xhrRequests.delete(this);
        if (this.status === 401) clear();
      }, { once: true });
    }
    return originalSend.apply(this, args);
  };
  window.WebSocket = class AuthenticatedSocket extends NativeSocket {
    constructor(url, protocols) {
      const api = isAPI(url);
      if (api) requireAuth();
      const list = protocols === undefined ? [] : typeof protocols === "string" ? [protocols] : [...protocols];
      // Browsers require a selected protocol when any are offered. Negotiate a
      // fixed application protocol so the server need not echo the credential.
      if (api) {
        if (!list.includes("wx-download.v1")) list.push("wx-download.v1");
        list.push("wx-auth." + token);
      }
      super(url, list);
      if (api) {
        sockets.add(this);
        this.addEventListener("close", () => {
          sockets.delete(this);
          if (!stopped && active()) refresh();
        }, { once: true });
      }
    }
  };
  window.addEventListener("pagehide", () => {
    stopped = true;
    clearTimeout(timer);
    clear();
  }, { once: true });
  refresh();
})();
