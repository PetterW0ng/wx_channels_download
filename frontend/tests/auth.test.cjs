const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../src/auth.js'), 'utf8');
const modulePromise = import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));

test('login sends contract fields and uses cookie credentials without persisting token', async () => {
  const { createAuthClient } = await modulePromise;
  let sent;
  const client = createAuthClient(async (url, options) => {
    sent = { url, options };
    return { ok: true, json: async () => ({ status: 'success', data: 'token' }) };
  });
  await client.login(' alice ', ' password ');
  assert.equal(sent.url, '/api/auth/login');
  assert.equal(sent.options.credentials, 'include');
  assert.deepEqual(JSON.parse(sent.options.body), { identity: 'alice', password: ' password ' });
});

test('HTTP failures and business failures reject the login', async () => {
  const { createAuthClient } = await modulePromise;
  for (const [ok, status, payload] of [
    [false, 401, { status: 'error', message: '账号或密码错误' }],
    [true, 200, { status: 'error', message: '登录失败' }],
  ]) {
    const client = createAuthClient(async () => ({ ok, status, json: async () => payload }));
    await assert.rejects(client.login('alice', 'bad'), error => error.message === payload.message && error.status === status);
  }
});

test('invalid JSON produces an actionable error', async () => {
  const { createAuthClient } = await modulePromise;
  const client = createAuthClient(async () => ({ ok: false, json: async () => { throw new Error('html'); } }));
  await assert.rejects(client.session(), /登录服务返回异常/);
});

const session = () => ({ expires_at: new Date(Date.now() + 60000).toISOString() });
const Result = { Err: error => ({ error }), Ok: data => ({ data }) };

test('core channels only start after login, once, independently of the selected page', async () => {
  const { createAuthenticatedRuntime } = await modulePromise;
  const calls = [];
  const models = ['download', 'scraper'].map(name => ({
    connect() { calls.push('connect:' + name); return Promise.resolve(true); },
    destroy() { calls.push('stop:' + name); },
  }));
  const runtime = createAuthenticatedRuntime({ apiOrigin: 'http://localhost:2022', client: {} });
  assert.throws(() => runtime.startCoreServices(models), /请先登录/);
  assert.deepEqual(calls, []);
  runtime.activate(session());
  await runtime.startCoreServices(models);
  runtime.startCoreServices(models);
  assert.deepEqual(calls, ['connect:download', 'connect:scraper']);
  runtime.stop();
  assert.deepEqual(calls.slice(2), ['stop:download', 'stop:scraper']);
});

test('socket handshake waits for session validation and uses the login origin', async () => {
  const { createAuthenticatedRuntime } = await modulePromise;
  let resolveSession;
  const client = { session: () => new Promise(resolve => { resolveSession = resolve; }) };
  const runtime = createAuthenticatedRuntime({ client, apiOrigin: 'http://localhost:2022' });
  let opens = 0, endpoint, aborted = false;
  const socket = { open: async options => {
    opens++; endpoint = options.endpoint;
    options.signal.addEventListener('abort', () => { aborted = true; });
    return Result.Ok({ close() {} });
  }};
  runtime.guardSocketClient(socket, Result);
  const options = { endpoint: 'ws://127.0.0.1:2022/ws/scraper', signal: new AbortController().signal, onClose() {} };
  assert.ok((await socket.open(options)).error);
  assert.equal(opens, 0);
  runtime.activate(session());
  const pending = socket.open(options);
  assert.equal(opens, 0);
  resolveSession({ status: 'success' });
  assert.equal((await pending).error, undefined);
  assert.equal(endpoint, 'ws://localhost:2022/ws/scraper');
  runtime.stop();
  assert.equal(aborted, true);
  assert.ok((await socket.open(options)).error);
  assert.equal(opens, 1);
});

test('401 during reconnect stops services without another WebSocket handshake', async () => {
  const { createAuthenticatedRuntime } = await modulePromise;
  let invalidations = 0, opens = 0, stopped = 0;
  const runtime = createAuthenticatedRuntime({
    apiOrigin: 'http://localhost:2022',
    client: { session: async () => { throw Object.assign(new Error('expired'), { status: 401 }); } },
    onInvalidated: () => invalidations++,
  });
  runtime.activate(session());
  runtime.onStop(() => stopped++);
  const socket = { open: async () => { opens++; return Result.Ok({}); } };
  runtime.guardSocketClient(socket, Result);
  await socket.open({ endpoint: '/ws/scraper', signal: new AbortController().signal });
  await socket.open({ endpoint: '/ws/scraper', signal: new AbortController().signal });
  assert.equal(opens, 0);
  assert.equal(stopped, 1);
  assert.equal(invalidations, 1);
});

test('HTTP requests are gated and stop on unauthorized responses', async () => {
  const { createAuthenticatedRuntime } = await modulePromise;
  let requests = 0, target;
  const runtime = createAuthenticatedRuntime({ apiOrigin: 'http://localhost:2022', client: {} });
  const http = {
    fetch: async options => {
      requests++; target = options.url;
      throw { response: { status: 401 } };
    },
    cancel() {},
  };
  runtime.guardHttpClient(http);
  await assert.rejects(http.fetch({ url: '/api/v1/download_task/create' }), /请先登录/);
  assert.equal(requests, 0);
  runtime.activate(session());
  await assert.rejects(http.fetch({ url: 'http://127.0.0.1:2022/api/v1/download_task/create' }));
  assert.equal(target, 'http://localhost:2022/api/v1/download_task/create');
  assert.equal(runtime.active, false);
  await assert.rejects(http.fetch({ url: '/api/v1/download_task/create' }), /请先登录/);
  assert.equal(requests, 1);
});

test('logout during a pending session check prevents a delayed handshake', async () => {
  const { createAuthenticatedRuntime } = await modulePromise;
  let resolveSession, opens = 0;
  const runtime = createAuthenticatedRuntime({
    apiOrigin: 'http://localhost:2022',
    client: { session: () => new Promise(resolve => { resolveSession = resolve; }) },
  });
  runtime.activate(session());
  const socket = { open: async () => { opens++; } };
  runtime.guardSocketClient(socket, Result);
  const pending = socket.open({ endpoint: '/ws/scraper', signal: new AbortController().signal });
  runtime.stop(); resolveSession({ status: 'success' });
  assert.ok((await pending).error);
  assert.equal(opens, 0);
});

test('native fetch requests stop on logout and cannot run before authentication', async () => {
  const { createAuthenticatedRuntime } = await modulePromise;
  const runtime = createAuthenticatedRuntime({ apiOrigin: 'http://localhost:2022', client: {} });
  let requests = 0, aborted = false;
  const fetcher = runtime.guardFetch(async (url, options) => {
    requests++;
    options.signal.addEventListener('abort', () => { aborted = true; });
    return new Promise(resolve => options.signal.addEventListener('abort', () => resolve({status: 200})));
  });
  await assert.rejects(fetcher('/api/browse_history/list'), /请先登录/);
  assert.equal(requests, 0);
  runtime.activate(session());
  const pending = fetcher('/api/browse_history/list');
  runtime.stop();
  await pending;
  assert.equal(aborted, true);
  assert.equal(requests, 1);
});
