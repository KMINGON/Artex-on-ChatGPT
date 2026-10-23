#!/usr/bin/env node
// Run in the Docker runtime: node /app/scripts/browser-runtime.test.cjs.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const https = require('node:https');
const net = require('node:net');
const readline = require('node:readline');
const { spawn, execFileSync } = require('node:child_process');

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'artex-browser-test-'));
const children = new Set();
const servers = new Set();
async function listen(server) {
  servers.add(server);
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', resolve).once('error', reject));
  return server.address().port;
}
async function freePort() {
  const server = net.createServer();
  const port = await listen(server);
  await new Promise((resolve) => server.close(resolve));
  servers.delete(server);
  return port;
}
function peer(script, args = [], env = {}) {
  const child = spawn(process.execPath, [path.join(__dirname, script), ...args], {
    env: { ...process.env, OPENAI_API_KEY: '', ...env }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  children.add(child);
  const pending = new Map();
  let sequence = 0;
  let readyResolve;
  const ready = new Promise((resolve) => { readyResolve = resolve; });
  const closed = new Promise((resolve) => child.once('exit', resolve));
  readline.createInterface({ input: child.stdout }).on('line', (line) => {
    let msg;
    try { msg = JSON.parse(line); } catch { return; }
    if (msg.ready) readyResolve();
    if (pending.has(msg.id)) {
      const { resolve, reject, timer } = pending.get(msg.id);
      pending.delete(msg.id); clearTimeout(timer);
      if (msg.error) reject(new Error(typeof msg.error === 'string' ? msg.error : JSON.stringify(msg.error)));
      else resolve(msg.result);
    }
  });
  child.once('exit', () => {
    children.delete(child);
    for (const { reject, timer } of pending.values()) { clearTimeout(timer); reject(new Error('fixture child exited')); }
    pending.clear();
  });
  child.stderr.on('data', () => {}); // Never mirror browser/profile data into a test log.
  const request = (body) => new Promise((resolve, reject) => {
    const id = ++sequence;
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`fixture timeout: ${body.action || body.method}`)); }, 30000);
    pending.set(id, { resolve, reject, timer });
    child.stdin.write(`${JSON.stringify({ id, ...body })}\n`);
  });
  return {
    request,
    ready: () => Promise.race([ready, closed.then(() => { throw new Error('bridge startup failed'); }), new Promise((_, reject) => { setTimeout(() => reject(new Error('bridge startup timeout')), 30000).unref(); })]),
    async close() { child.stdin.end(); await Promise.race([closed, new Promise((resolve) => { setTimeout(() => { child.kill('SIGKILL'); resolve(); }, 5000).unref(); })]); },
  };
}
async function browser(name, options = {}, env = {}) {
  const port = await freePort();
  const bridge = peer('browser-session.cjs', [], { ARTEX_BROWSER_OPTIONS: JSON.stringify({ directory: path.join(root, name), port, ...options }), ...env });
  await bridge.ready();
  const mcp = peer('browser-mcp.cjs', ['--cdp-endpoint', `http://127.0.0.1:${port}`, '--output-dir', path.join(root, name, 'artifacts')]);
  await mcp.request({ jsonrpc: '2.0', method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'artex-browser-fixture', version: '1' } } });
  const call = async (tool, args) => {
    const result = await mcp.request({ jsonrpc: '2.0', method: 'tools/call', params: { name: tool, arguments: args } });
    if (result.isError) throw new Error((result.content || []).filter((c) => c.type === 'text').map((c) => c.text).join('\n'));
    return result;
  };
  return {
    bridge, call,
    async close() { await bridge.close(); await mcp.close(); },
  };
}
function text(result) { return result.content.filter((c) => c.type === 'text').map((c) => c.text).join('\n'); }

async function main() {
  const fixture = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://fixture');
    if (url.pathname === '/data') { res.setHeader('Content-Type', 'application/json'); res.end('{"value":"js-fetch-ok"}'); return; }
    res.setHeader('Content-Type', 'text/html');
    const name = url.searchParams.get('name');
    res.end(`<!doctype html><title>ARTEX fixture</title><form><input id="human" aria-label="인증 입력"><button>완료</button></form><div id="result"></div><script>${name ? `document.cookie='sid=${name}; path=/';sessionStorage.setItem('sid','${name}');localStorage.setItem('sid','${name}');` : ''}fetch('/data').then(r=>r.json()).then(x=>document.querySelector('#result').textContent=x.value);document.querySelector('form').onsubmit=e=>{e.preventDefault();document.cookie='auth=manual; path=/';document.querySelector('#result').textContent='human:'+document.querySelector('#human').value}</script>`);
  });
  const port = await listen(fixture);
  const base = `http://127.0.0.1:${port}`;
  const sessions = await Promise.all(['alpha', 'beta', 'gamma'].map((name) => browser(name)));
  await Promise.all(sessions.map(async (session, index) => {
    const name = ['alpha', 'beta', 'gamma'][index];
    await session.call('browser_navigate', { url: `${base}/?name=${name}` });
    const state = text(await session.call('browser_evaluate', { function: 'async () => { await new Promise(r=>setTimeout(r,150)); return {cookie:document.cookie,local:localStorage.getItem("sid"),session:sessionStorage.getItem("sid"),dom:document.querySelector("#result").textContent}; }' }));
    assert.ok(state.includes(`sid=${name}`)); assert.ok(state.includes('js-fetch-ok'));
    for (const other of ['alpha', 'beta', 'gamma'].filter((v) => v !== name)) assert.ok(!state.includes(other));
    await session.call('browser_navigate', { url: `${base}/probe` });
  }));
  const first = sessions[0];
  await first.call('browser_evaluate', { function: '() => document.querySelector("#human").focus()' });
  await first.bridge.request({ action: 'type', text: 'manual-fixture' });
  await first.bridge.request({ action: 'key', key: 'Enter' });
  assert.ok(text(await first.call('browser_evaluate', { function: '() => document.querySelector("#result").textContent' })).includes('human:manual-fixture'));
  const image = await first.call('browser_take_screenshot', { type: 'png' });
  assert.ok(image.content.some((c) => c.type === 'image' && c.mimeType === 'image/png' && c.data.length > 100));
  await Promise.all(sessions.map((s) => s.close()));
  const saved = JSON.parse(fs.readFileSync(path.join(root, 'alpha', 'session.json'), 'utf8'));
  assert.ok(saved.pages.some((p) => p.storage?.sid === 'alpha'), 'sessionStorage was not saved before shutdown');
  const restored = await browser('alpha');
  const state = text(await restored.call('browser_evaluate', { function: '() => ({cookie:document.cookie,local:localStorage.getItem("sid"),session:sessionStorage.getItem("sid")})' }));
  assert.ok(state.includes('sid=alpha')); assert.ok(state.includes('auth=manual')); assert.equal((state.match(/"alpha"/g) || []).length, 2, state);
  await restored.call('browser_evaluate', { function: '() => sessionStorage.setItem("sid","rotated")' });
  await restored.call('browser_navigate', { url: `${base}/probe` });
  assert.ok(text(await restored.call('browser_evaluate', { function: '() => sessionStorage.getItem("sid")' })).includes('rotated'));
  await restored.call('browser_close', {});
  await restored.call('browser_navigate', { url: `${base}/probe` });
  assert.ok(text(await restored.call('browser_evaluate', { function: '() => document.cookie' })).includes('sid=alpha'));
  await restored.close();
  console.log('PASS: 3 concurrent isolated profiles, JS/fetch, same-page human input, MCP screenshot image, session-cookie/localStorage/sessionStorage restart and token rotation');

  const ca = path.join(root, 'ca.crt'); const caKey = path.join(root, 'ca.key');
  const key = path.join(root, 'site.key'); const csr = path.join(root, 'site.csr'); const cert = path.join(root, 'site.crt');
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=ARTEX fixture CA', '-addext', 'basicConstraints=critical,CA:TRUE', '-keyout', caKey, '-out', ca], { stdio: 'ignore' });
  execFileSync('openssl', ['req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=127.0.0.1', '-keyout', key, '-out', csr], { stdio: 'ignore' });
  fs.writeFileSync(path.join(root, 'ext'), 'subjectAltName=IP:127.0.0.1\n');
  execFileSync('openssl', ['x509', '-req', '-in', csr, '-CA', ca, '-CAkey', caKey, '-CAcreateserial', '-out', cert, '-days', '1', '-extfile', path.join(root, 'ext')], { stdio: 'ignore' });
  const secure = https.createServer({ key: fs.readFileSync(key), cert: fs.readFileSync(cert) }, (_, res) => res.end('<title>trusted-fixture</title>trusted'));
  const securePort = await listen(secure);
  let tunnels = 0;
  const proxy = http.createServer();
  proxy.on('connect', (req, socket, head) => {
    tunnels++;
    const [host, port] = req.url.split(':');
    const upstream = net.connect(Number(port), host, () => { socket.write('HTTP/1.1 200 Connection Established\r\n\r\n'); if (head.length) upstream.write(head); upstream.pipe(socket); socket.pipe(upstream); });
    upstream.on('error', () => socket.destroy()); socket.on('error', () => upstream.destroy());
  });
  const proxyPort = await listen(proxy);
  const untrusted = await browser('untrusted');
  await assert.rejects(() => untrusted.call('browser_navigate', { url: `https://127.0.0.1:${securePort}` }), /CERT_AUTHORITY_INVALID/);
  await untrusted.close();
  const trusted = await browser('trusted', { proxy: `http://127.0.0.1:${proxyPort}` }, { NODE_EXTRA_CA_CERTS: ca });
  await trusted.call('browser_navigate', { url: `https://127.0.0.1:${securePort}` });
  assert.ok(tunnels > 0, 'localhost bypassed the configured proxy');
  assert.ok(text(await trusted.call('browser_evaluate', { function: '() => document.title' })).includes('trusted-fixture'));
  await trusted.close();
  console.log('PASS: untrusted certificate rejected, explicit CA trusted by Chromium, localhost HTTPS traversed configured proxy');
}
main().then(() => { process.exitCode = 0; }).catch((err) => { console.error(err.message); process.exitCode = 1; }).finally(async () => {
  for (const child of children) child.kill('SIGTERM');
  for (const server of servers) server.closeAllConnections?.();
  for (const server of servers) server.close();
  await new Promise((resolve) => setTimeout(resolve, 300));
  fs.rmSync(root, { recursive: true, force: true });
});
