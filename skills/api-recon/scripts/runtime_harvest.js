#!/usr/bin/env node
/*
 * runtime_harvest.js <config.json>
 *
 * 참고 템플릿 — 범용 완성본이 아닙니다. 실행 전에 대상 사이트에 맞게 config.json과 스크립트 로직을 수정하세요:
 *   cookies/localStorage, neutralize 필드, stubs 구조, loginUrlPattern, apiPattern
 *
 * Drives a headless browser through an authorized SPA to capture the live API
 * surface (method + url + body) by defeating three client-side gates:
 *   1. render gate    -> inject fake auth state (cookies / localStorage)
 *   2. interceptor gate-> rewrite the "unauthorized" code field to success
 *   3. content gate   -> stub the menu/permission endpoint with a full-feature payload
 *
 * ARTEX Docker의 Playwright/Chromium을 재사용합니다. 로컬은 npm ci 후 npx playwright install chromium.
 *
 * config.json schema (all fields optional except baseUrl):
 * {
 *   "baseUrl": "https://target/",
 *   "chromium": "",                             // optional; env CHROMIUM or Playwright bundled Chromium
 *   "cookies":   [{"name":"auth","value":"b64json:{\"id\":1,\"username\":\"admin\"}"}],
 *   "localStorage": {"token":"x","isLogin":"1"},
 *   "neutralize": { "fields":["response_code","code","errno","ret"], "success":0,
 *                   "flags":{"success":true,"message":"ok"} },
 *   "forward": true,                            // true: 응답 업무 코드 보정; false: apiPattern 요청만 stub(HTML/JS·패턴 밖 요청은 전송 가능)
 *   "loginUrlPattern": "/login",               // navigations matching this are suppressed as a fallback
 *   "stubs": [ {"match":"permission|menu|role", "body": { ...full-feature menu... }} ],
 *   "routes": ["/dashboard","/device", ...],   // from routes.txt or the forged menu
 *   "apiPattern": "/api/|/rest/|/graphql",      // what counts as an API call to record
 *   "proxy": "http://127.0.0.1:8080",           // optional; also HTTP_PROXY / HTTPS_PROXY
 *   "waitUntil": "domcontentloaded",            // 대형 SPA는 networkidle 대기보다 권장(networkidle2/0은 호환 별칭)
 *   "routeTimeout": 12000,
 *   "waitMs": 1200, "perRouteMs": 900, "headless": true,
 *   "ignoreHTTPSErrors": false                  // explicit self-signed lab targets only
 * }
 */
const fs = require('fs');
const path = require('path');
const os = require('os');

let chromium;
try { ({ chromium } = require('playwright')); }
catch (e) { console.error('[!] Playwright가 없습니다. ARTEX Docker에서 실행하거나 scripts/에서 npm ci 후 npx playwright install chromium을 실행하세요.'); process.exit(1); }

function loadCfg(p) {
  const c = JSON.parse(fs.readFileSync(p, 'utf8'));
  c.baseUrl || (() => { throw new Error("config.baseUrl required"); })();
  c.chromium = c.chromium || process.env.CHROMIUM || process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined;
  c.neutralize = c.neutralize || { fields: ['response_code', 'code', 'errno', 'ret', 'status'], success: 0, flags: { success: true } };
  c.forward = c.forward !== false;
  c.apiPattern = new RegExp(c.apiPattern || '/api/|/rest/|/service/|/graphql|/gateway/');
  c.loginUrlPattern = c.loginUrlPattern || '/login';
  c.routes = c.routes || ['/'];
  c.waitMs = c.waitMs ?? 1200; c.perRouteMs = c.perRouteMs ?? 900;
  c.headless = c.headless !== false;
  c.waitUntil = c.waitUntil === 'networkidle2' || c.waitUntil === 'networkidle0' ? 'networkidle' : (c.waitUntil || 'domcontentloaded');
  c.routeTimeout = c.routeTimeout || 12000;
  c.captureResponses = c.captureResponses !== false; // record response body samples (forward mode)
  c.recordWs = c.recordWs !== false;                 // record WebSocket frames + SSE endpoints
  c.respMax = c.respMax || 600;                       // truncation length for captured bodies
  c.proxy = c.proxy || process.env.HTTP_PROXY || process.env.HTTPS_PROXY || '';
  (c.stubs || []).forEach(s => s._re = new RegExp(s.match));
  return c;
}

function cookieValue(v) {
  if (typeof v === 'string' && v.startsWith('b64json:'))
    return Buffer.from(v.slice(8)).toString('base64');
  if (typeof v === 'string' && v.startsWith('json:'))
    return v.slice(5);
  return v;
}

function neutralize(txt, n) {
  try {
    const j = JSON.parse(txt);
    if (j && typeof j === 'object') {
      for (const f of n.fields) if (f in j) j[f] = n.success;
      Object.assign(j, n.flags || {});
      return JSON.stringify(j);
    }
  } catch (e) {}
  return txt;
}

(async () => {
  const cfg = loadCfg(process.argv[2] || 'config.json');
  const origin = new URL(cfg.baseUrl).origin;
  const rec = [];           // {m,u,b,resp,ct}
  const chunks = new Set();
  const ws = [];            // {url,dir,data}  WebSocket frames
  const sse = new Set();    // SSE (text/event-stream) endpoints

  const proxyURL = cfg.proxy ? new URL(cfg.proxy) : null;
  // Isolate Chromium's CA store, including when several workers run this script.
  const browserHome = fs.mkdtempSync(path.join(os.tmpdir(), 'artex-recon-browser-'));
  const runtimePath = process.env.ARTEX_BROWSER_RUNTIME || '/opt/artex-browser/runtime.cjs';
  const localRuntime = path.resolve(__dirname, '../../../scripts/browser-runtime.cjs');
  const helper = fs.existsSync(runtimePath) ? runtimePath : localRuntime;
  let browser;
  try {
  if (fs.existsSync(helper)) require(helper).prepareBrowserHome(browserHome);
  else if (process.env.NODE_EXTRA_CA_CERTS) throw new Error('기록 프록시 CA 런타임을 찾을 수 없습니다. ARTEX_BROWSER_RUNTIME을 지정하세요.');
  browser = await chromium.launch({
    executablePath: cfg.chromium, headless: cfg.headless,
    env: { ...process.env, HOME: browserHome },
    proxy: proxyURL ? {
      server: `${proxyURL.protocol}//${proxyURL.host}`,
      username: decodeURIComponent(proxyURL.username), password: decodeURIComponent(proxyURL.password)
    } : undefined,
    args: cfg.proxy ? ['--proxy-bypass-list=<-loopback>'] : [],
  });
  const context = await browser.newContext({
    ignoreHTTPSErrors: cfg.ignoreHTTPSErrors === true,
    serviceWorkers: 'block', // service workers otherwise bypass request routing
  });
  const page = await context.newPage();

  // ---- WebSocket frame capture via CDP (fetch/XHR interception can't see WS) ----
  if (cfg.recordWs) {
    try {
      const cdp = await context.newCDPSession(page);
      await cdp.send('Network.enable');
      const wsUrl = {}; // requestId -> url
      cdp.on('Network.webSocketCreated', e => { wsUrl[e.requestId] = e.url; });
      const onFrame = dir => e => {
        const p = e.response && e.response.payloadData;
        if (p != null) ws.push({ url: (wsUrl[e.requestId] || '').replace(origin, ''), dir, data: String(p).slice(0, cfg.respMax) });
      };
      cdp.on('Network.webSocketFrameSent', onFrame('send'));
      cdp.on('Network.webSocketFrameReceived', onFrame('recv'));
    } catch (e) { console.log('[ws] CDP capture unavailable:', e.message); }
  }

  // inject localStorage on every document
  if (cfg.localStorage) {
    await context.addInitScript((kv) => {
      try { for (const k in kv) localStorage.setItem(k, kv[k]); } catch (e) {}
    }, cfg.localStorage);
  }
  // fallback: block full-page navigations to the login url
  await context.addInitScript((pat) => {
    const bad = u => { try { return String(u).indexOf(pat) >= 0; } catch (e) { return false; } };
    try {
      const d = Object.getOwnPropertyDescriptor(Location.prototype, 'href');
      Object.defineProperty(Location.prototype, 'href', { configurable: true,
        get() { return d.get.call(this); },
        set(v) { if (bad(v)) return; return d.set.call(this, v); } });
      const a = Location.prototype.assign, r = Location.prototype.replace;
      Location.prototype.assign = function (v) { if (bad(v)) return; return a.call(this, v); };
      Location.prototype.replace = function (v) { if (bad(v)) return; return r.call(this, v); };
    } catch (e) {}
  }, cfg.loginUrlPattern);

  await page.route('**/*', async (route) => {
    const req = route.request();
    const u = req.url(), m = req.method();
    if (/\.js(\?|$)/.test(u) && /\/assets\/|\/static\/|\/js\//.test(u)) chunks.add(u.split('/').pop());

    // suppress fallback login navigations (after the app has bootstrapped)
    if (req.isNavigationRequest() && req.frame() === page.mainFrame()
        && u.includes(cfg.loginUrlPattern) && rec.length > 3) {
      return route.fulfill({ status: 204, body: '' });
    }
    if (!cfg.apiPattern.test(u)) return route.continue();

    const entry = { m, u: u.replace(origin, '').split('?')[0], full: u, b: req.postData() ? req.postData().slice(0, 400) : null };
    rec.push(entry);

    // explicit stubs (menu / permission forgery) win
    const stub = (cfg.stubs || []).find(s => s._re.test(u));
    if (stub) { entry.responseSource = 'configured_stub'; return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(stub.body) }); }

    // SSE: forwarding a text/event-stream would hang the handler — record + short-circuit
    const accept = (req.headers().accept || '');
    if (/text\/event-stream/.test(accept)) { sse.add(entry.u); entry.responseSource = 'sse_stub'; return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' }); }

    if (!cfg.forward) {
      entry.responseSource = 'offline_stub';
      return route.fulfill({ status: 200, contentType: 'application/json',
        body: neutralize('{"data":{},"list":[],"total":0}', cfg.neutralize) });
    }
    // forward real request, then rewrite the unauthorized code field
    try {
      // Playwright uses this browser context's cookies and configured proxy.
      // Node fetch without a dispatcher would silently bypass the browser proxy.
      const r = await route.fetch({ timeout: cfg.routeTimeout, maxRedirects: 0 });
      const t = await r.text();
      entry.responseSource = 'forwarded_modified';
      entry.actualStatus = r.status();
      if (cfg.captureResponses) { entry.resp = t.slice(0, cfg.respMax); entry.ct = r.headers()['content-type'] || ''; }
      if (/text\/event-stream/.test(r.headers()['content-type'] || '')) sse.add(entry.u);
      await route.fulfill({ status: 200, contentType: 'application/json', body: neutralize(t, cfg.neutralize) });
      await r.dispose();
    } catch (e) {
      entry.responseSource = 'error_stub';
      entry.forwardError = e.message;
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"response_code":0,"code":0,"data":{}}' });
    }
  });

  // set auth cookies
  if (cfg.cookies) {
    for (const c of cfg.cookies)
      await context.addCookies([{ name: c.name, value: cookieValue(c.value), domain: new URL(cfg.baseUrl).hostname, path: c.path || '/' }]);
  }

  // boot
  await page.goto(cfg.baseUrl, { waitUntil: cfg.waitUntil, timeout: 45000 }).catch(e => console.log('[goto]', e.message));
  await new Promise(r => setTimeout(r, cfg.waitMs));
  const shell = await page.evaluate(() => ({
    url: location.href,
    loginForm: !!document.querySelector('input[type=password]'),
    links: [...document.querySelectorAll('a[href^="/"]')].map(a => a.getAttribute('href'))
  }));
  console.log(`[*] after boot: url=${shell.url}  loginForm=${shell.loginForm}`);
  if (shell.loginForm) console.log('[!] still on login — recheck render-gate facts (cookies/localStorage) in config');

  // discovered menu links extend the route list
  const routes = [...new Set([...cfg.routes, ...shell.links.filter(h => h && h.length > 1)])];
  const perRoute = {};
  for (const rt of routes) {
    const before = rec.length;
    try { await page.goto(origin + rt, { waitUntil: cfg.waitUntil, timeout: cfg.routeTimeout }); } catch (e) {}
    await new Promise(r => setTimeout(r, cfg.perRouteMs));
    const fresh = [...new Set(rec.slice(before).map(r => r.m + ' ' + r.u))];
    perRoute[rt] = fresh;
  }

  const uniq = [...new Set(rec.map(r => r.m + ' ' + r.u))].sort();
  const wsUniq = [...new Set(ws.map(f => f.url))].filter(Boolean).sort();
  const outdir = path.dirname(process.argv[2] || '.');
  fs.writeFileSync(path.join(outdir, 'runtime_api.json'),
    JSON.stringify({ evidenceKind: 'frontend_discovery_mock', shell, uniq, perRoute, rec, ws, wsEndpoints: wsUniq, sse: [...sse] }, null, 2));

  // merge with static
  let merged = new Set(uniq.map(x => x.split(' ')[1]));
  const staticFile = path.join(outdir, 'api_static.txt');
  if (fs.existsSync(staticFile)) fs.readFileSync(staticFile, 'utf8').split('\n').filter(Boolean).forEach(p => merged.add(p));
  fs.writeFileSync(path.join(outdir, 'api_merged.txt'), [...merged].sort().join('\n'));

  console.log(`[+] runtime endpoints (with method): ${uniq.length}`);
  console.log(`[+] chunks seen at runtime: ${chunks.size}`);
  if (cfg.recordWs) console.log(`[+] websocket endpoints: ${wsUniq.length} (${ws.length} frames) | SSE endpoints: ${sse.size}`);
  if (cfg.captureResponses) console.log(`[+] response bodies captured for ${rec.filter(r => r.resp != null).length}/${rec.length} calls`);
  console.log(`[+] merged unique paths: ${merged.size}  -> api_merged.txt`);
  console.log(`[+] full detail -> runtime_api.json (per-route + bodies + ws frames + sse)`);
  } finally {
    try { if (browser) await browser.close(); }
    finally { fs.rmSync(browserHome, { recursive: true, force: true }); }
  }
})().catch(error => { console.error('[!] 동적 조사 실패:', error.message); process.exitCode = 1; });
