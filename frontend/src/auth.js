// Authentication is checked before the application and its background clients load.
export function createAuthClient(fetcher, baseURL = "") {
  async function request(path, options = {}) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 20000);
    try {
      const response = await fetcher(baseURL + "/api/auth/" + path, {
        ...options,
        credentials: "include",
        cache: "no-store",
        signal: controller.signal,
        headers: { "Content-Type": "application/json" },
      });
      let payload;
      try { payload = await response.json(); } catch {
        throw new Error("登录服务返回异常，请稍后重试");
      }
      if (!response.ok || payload.status !== "success") {
        const error = new Error(payload.message || "认证失败，请重试");
        error.status = response.status;
        throw error;
      }
      return payload;
    } catch (error) {
      if (error.name === "AbortError") throw new Error("登录请求超时，请重试");
      throw error;
    } finally {
      clearTimeout(timeout);
    }
  }
  return {
    session: () => request("session"),
    login: (identity, password) => request("login", {
      method: "POST", body: JSON.stringify({ identity: identity.trim(), password }),
    }),
    logout: () => request("logout", { method: "POST" }),
  };
}

// Owns the lifetime of all authenticated transports, independently of the page route.
export function createAuthenticatedRuntime({ client, apiOrigin, onInvalidated = () => {} }) {
  let active = false;
  let checking = null;
  let coreStarted = false;
  let expiryTimer;
  const cleanup = new Set();
  const unauthorized = () => Object.assign(new Error("请先登录"), { status: 401 });
  function onStop(callback) {
    cleanup.add(callback);
    return () => cleanup.delete(callback);
  }
  function stop() {
    active = false;
    clearTimeout(expiryTimer);
    const callbacks = [...cleanup];
    cleanup.clear();
    for (const callback of callbacks) {
      try { callback(); } catch (error) { console.error(error); }
    }
  }
  function invalidate() {
    if (!active) return;
    stop();
    onInvalidated();
  }
  function assertActive() {
    if (!active) throw unauthorized();
  }
  async function check() {
    assertActive();
    if (!checking) {
      checking = client.session().catch((error) => {
        if (error.status === 401) invalidate();
        throw error;
      }).finally(() => { checking = null; });
    }
    await checking;
    assertActive();
  }
  function endpoint(value, websocket = false) {
    const url = new URL(value, apiOrigin);
    const origin = new URL(apiOrigin);
    url.host = origin.host;
    url.protocol = websocket ? (origin.protocol === "https:" ? "wss:" : "ws:") : origin.protocol;
    return url.href;
  }
  function guardHttpClient(http) {
    const fetch = http.fetch.bind(http);
    http.fetch = async (options) => {
      assertActive();
      const release = onStop(() => { if (options.id) http.cancel(options.id); });
      try {
        return await fetch({ ...options, url: endpoint(options.url) });
      } catch (error) {
        if ((error.response?.status || error.status) === 401) invalidate();
        throw error;
      } finally { release(); }
    };
  }
  function guardSocketClient(socket, Result) {
    const open = socket.open.bind(socket);
    socket.open = async (options) => {
      try { await check(); assertActive(); } catch (error) { return Result.Err(error); }
      const abort = new AbortController();
      const cancel = () => abort.abort();
      if (options.signal.aborted) return Result.Err("连接已取消");
      options.signal.addEventListener("abort", cancel, { once: true });
      const release = onStop(cancel);
      const detach = () => {
        release();
        options.signal.removeEventListener("abort", cancel);
      };
      try {
        const result = await open({
          ...options, endpoint: endpoint(options.endpoint, true), hostname: "", signal: abort.signal,
          onClose(event) { detach(); options.onClose(event); },
        });
        if (result.error) detach();
        if (!active) {
          result.data?.close(1000, "session ended");
          detach();
          return Result.Err(unauthorized());
        }
        return result;
      } catch (error) { detach(); return Result.Err(error); }
    };
  }
  function guardFetch(fetcher) {
    return async (input, options = {}) => {
      const url = new URL(typeof input === "string" || input instanceof URL ? input : input.url, apiOrigin);
      const core = url.origin === apiOrigin && (url.pathname.startsWith("/api/") || url.pathname === "/mcp");
      if (!core || url.pathname.startsWith("/api/auth/")) return fetcher(input, options);
      assertActive();
      const abort = new AbortController();
      const signal = options.signal || input.signal;
      const cancel = () => abort.abort();
      if (signal?.aborted) abort.abort();
      signal?.addEventListener("abort", cancel, { once: true });
      const release = onStop(cancel);
      try {
        const response = await fetcher(input, { ...options, signal: abort.signal });
        if (response.status === 401) invalidate();
        return response;
      } finally {
        release();
        signal?.removeEventListener("abort", cancel);
      }
    };
  }
  return {
    apiOrigin, check, stop, onStop, guardHttpClient, guardSocketClient, guardFetch,
    get active() { return active; },
    activate(session) {
      active = true;
      clearTimeout(expiryTimer);
      expiryTimer = setTimeout(invalidate, Math.max(0, Date.parse(session.expires_at) - Date.now()));
    },
    startCoreServices(models) {
      assertActive();
      if (coreStarted) return;
      coreStarted = true;
      for (const model of models) onStop(() => model.destroy());
      // Start both channels after authentication, even when opening a history/detail page.
      return Promise.allSettled(models.map((model) => model.connect()));
    },
  };
}

export async function startAuthenticatedApp() {
  const root = document.querySelector("#root");
  const client = createAuthClient(window.fetch.bind(window), window.location.origin);
  root.innerHTML = '<main class="auth-page"><p role="status">正在检查登录状态…</p></main>';
  let session;
  try {
    session = (await client.session()).data;
  } catch (error) {
    showLogin(root, client, error.status === 401 ? "" : error.message);
    return;
  }
  const runtime = createAuthenticatedRuntime({
    client, apiOrigin: window.location.origin,
    onInvalidated: () => window.location.reload(),
  });
  runtime.activate(session);
  window.fetch = runtime.guardFetch(window.fetch.bind(window));
  window.auth = Object.assign(runtime, {
    username: session.username,
    async logout() {
      try {
        await client.logout();
        runtime.stop();
        window.location.reload();
      } catch (error) {
        window.alert(error.message || "退出失败，请重试");
      }
    },
  });
  window.addEventListener("pagehide", () => runtime.stop(), { once: true });
  window.addEventListener("pageshow", (event) => { if (event.persisted) window.location.reload(); });
  root.replaceChildren();
  try {
    // Resolve relative to this module so injected/local asset paths also work.
    const entry = new URL("./index.js", import.meta.url);
    entry.search = new URL(import.meta.url).search;
    await import(entry.href);
  } catch {
    runtime.stop();
    root.textContent = "应用加载失败，请刷新页面重试。";
    return;
  }
  // A reload unmounts pages and closes their sockets after expiry or logout in another tab.
  const timer = setInterval(async () => {
    try { await runtime.check(); } catch (error) {
      if (error.status === 401 || Date.now() >= Date.parse(session.expires_at)) {
        clearInterval(timer);
        runtime.stop();
        window.location.reload();
      }
    }
  }, 30000);
  runtime.onStop(() => clearInterval(timer));
}

function showLogin(root, client, initialError) {
  root.innerHTML = `
    <main class="auth-page">
      <section class="auth-card" aria-labelledby="auth-title">
        <img class="auth-logo" src="public/logo.png" alt="D&M" />
        <h1 id="auth-title">登录 Download & Manage</h1>
        <p class="auth-description">登录你的账号，开始下载与管理内容。</p>
        <form class="auth-form">
          <label for="auth-identity">账号或邮箱</label>
          <input id="auth-identity" name="identity" autocomplete="username" required maxlength="254" placeholder="请输入账号或邮箱" />
          <label for="auth-password">密码</label>
          <input id="auth-password" name="password" type="password" autocomplete="current-password" required placeholder="请输入密码" />
          <p class="auth-error" role="alert" aria-live="polite"></p>
          <button type="submit">登录</button>
        </form>
      </section>
    </main>`;
  const form = root.querySelector("form");
  const identity = root.querySelector("#auth-identity");
  const password = root.querySelector("#auth-password");
  const button = root.querySelector("button");
  const message = root.querySelector(".auth-error");
  message.textContent = initialError;
  identity.focus();
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (button.disabled) return;
    button.disabled = true;
    button.textContent = "正在登录…";
    message.textContent = "";
    try {
      await client.login(identity.value, password.value);
      password.value = "";
      // Verify the HttpOnly cookie was accepted before loading any app pages.
      await client.session();
      window.location.reload();
    } catch (error) {
      password.value = "";
      message.textContent = error.message || "网络连接失败，请重试";
      password.focus();
    } finally {
      button.disabled = false;
      button.textContent = "登录";
    }
  });
}
