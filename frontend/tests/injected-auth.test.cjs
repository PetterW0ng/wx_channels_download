const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../inject/auth.js'), 'utf8');

function harness() {
  const window = new EventTarget();
  const calls = [], sockets = [], timers = new Map();
  let grant = null, timerID = 0;
  class Socket extends EventTarget {
    constructor(url, protocols) { super(); this.url = url; this.protocols = protocols; sockets.push(this); }
    close() { this.closed = true; this.dispatchEvent(new Event('close')); }
  }
  class XHR extends EventTarget {
    constructor() { super(); this.headers = {}; }
    open(method, url) { this.url = url; }
    setRequestHeader(key, value) { this.headers[key] = value; }
    send() { calls.push({type: 'xhr', url: this.url, headers: this.headers}); }
    abort() { this.aborted = true; this.dispatchEvent(new Event('loadend')); }
  }
  window.__d_config = {injectedAuthRequired: true, apiOrigin: 'http://127.0.0.1:2022'};
  window.location = {href: 'https://channels.weixin.qq.com/web/pages/feed'};
  window.XMLHttpRequest = XHR;
  window.WebSocket = Socket;
  window.fetch = async (url, options) => {
    calls.push({type: 'fetch', url, options});
    if (url === '/__assets/auth/session') return {json: async () => grant ? {status: 'success', data: grant} : {status: 'error'}};
    return {status: 200};
  };
  vm.runInNewContext(source, {window, URL, Headers, Event, AbortController, console,
    setTimeout(fn) { timers.set(++timerID,fn); return timerID; }, clearTimeout(id) {timers.delete(id);} });
  return {window,calls,sockets,timers,login() {grant={token:'test-grant',expires_at:new Date(Date.now()+60000).toISOString()};},logout() {grant=null;}};
}

test('embedded calls wait for login, then authenticate fetch, XHR and both sockets', async () => {
  const h=harness();
  await h.window.WXAuth.refresh();
  assert.equal(h.window.WXAuth.active(),false);
  await assert.rejects(h.window.fetch('http://127.0.0.1:2022/api/v1/download_task/create'), /请先/);
  assert.throws(() => new h.window.WebSocket('ws://127.0.0.1:2022/ws/channels'), /请先/);
  assert.equal(h.sockets.length,0);
  assert.ok(h.calls.every(c=>c.url==='/__assets/auth/session'));
  h.login(); await h.window.WXAuth.refresh();
  assert.equal(h.window.WXAuth.active(),true);
  await h.window.fetch('http://127.0.0.1:2022/api/v1/download_task/create', {method:'POST'});
  assert.equal(h.calls.at(-1).options.headers.get('Authorization'),'Bearer test-grant');
  const xhr=new h.window.XMLHttpRequest(); xhr.open('POST','http://127.0.0.1:2022/api/browse_history/create'); xhr.send();
  assert.equal(h.calls.at(-1).headers.Authorization,'Bearer test-grant');
  for (const endpoint of ['/ws/channels','/ws/v1/download_task']) {
    new h.window.WebSocket('ws://127.0.0.1:2022'+endpoint);
    assert.deepEqual(Array.from(h.sockets.at(-1).protocols),['wx-download.v1','wx-auth.test-grant']);
    assert.ok(!h.sockets.at(-1).url.includes('test-grant'));
  }
  h.logout(); await h.window.WXAuth.refresh();
  assert.equal(h.window.WXAuth.active(),false);
  assert.ok(h.sockets.every(s=>s.closed));
  assert.equal(xhr.aborted,true);
  assert.throws(()=>new h.window.WebSocket('ws://127.0.0.1:2022/ws/channels'),/请先/);
});

test('grant is not attached to WeChat platform traffic or unrelated hosts',async()=>{
 const h=harness(); h.login(); await h.window.WXAuth.refresh();
 await h.window.fetch('https://channels.weixin.qq.com/api/platform/feed',{headers:{Accept:'application/json'}});
 assert.equal(h.calls.at(-1).options.headers.Authorization,undefined);
 await h.window.fetch('https://other.example/api/download',{});
 assert.equal(h.calls.at(-1).options.headers,undefined);
 const xhr=new h.window.XMLHttpRequest(); xhr.open('GET','https://other.example/api/download');xhr.send();
 assert.equal(h.calls.at(-1).headers.Authorization,undefined);
 new h.window.WebSocket('wss://other.example/ws/events','platform-protocol');
 assert.deepEqual(Array.from(h.sockets.at(-1).protocols),['platform-protocol']);
});

test('page close stops renewal and closes embedded connections',async()=>{
 const h=harness();h.login();await h.window.WXAuth.refresh();
 new h.window.WebSocket('ws://127.0.0.1:2022/ws/channels');
 h.window.dispatchEvent(new Event('pagehide'));
 assert.equal(h.window.WXAuth.active(),false);
 assert.equal(h.timers.size,0);
 assert.ok(h.sockets.every(s=>s.closed));
});
