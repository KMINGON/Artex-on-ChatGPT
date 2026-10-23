const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const net = require('node:net');
const crypto = require('node:crypto');
const { spawn } = require('node:child_process');

let available = true;
try { require.resolve('playwright'); } catch { available = false; }
const listen = server => new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const close = server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });

// Real Chromium test: a name resolvable only by the proxy proves both page
// navigation and intercepted API forwarding retain the configured egress route.
test('Playwright harvest preserves proxy/cookies, mock labels and CDP WebSocket frames', { skip: !available, timeout: 45000 }, async () => {
  const seen = [];
  const sockets = new Set();
  const fixture = http.createServer((req, res) => {
    if (req.url.startsWith('/api/')) {
      seen.push({ url: req.url, cookie: req.headers.cookie || '' });
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ code: 401, data: { fixture: true } }));
      return;
    }
    res.setHeader('content-type', 'text/html');
    res.end(`<html><body><div id="app"></div><script>
      fetch('/api/data?storage=' + localStorage.getItem('fixture')).then(r => r.json()).then(data => {
        document.querySelector('#app').textContent = data.code;
        fetch('/api/menu');
      });
      new EventSource('/api/events');
      const socket = new WebSocket('ws://' + location.host + '/socket');
      socket.addEventListener('open', () => socket.send('fixture-client'));
    </script></body></html>`);
  });
  fixture.on('upgrade', (req, socket) => {
    sockets.add(socket); socket.on('close', () => sockets.delete(socket));
    socket.on('error', () => {});
    const accept = crypto.createHash('sha1').update(req.headers['sec-websocket-key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
    socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
    const data = Buffer.from('fixture-server');
    socket.write(Buffer.concat([Buffer.from([0x81, data.length]), data]));
  });
  await listen(fixture);
  const port = fixture.address().port;
  const proxied = [];
  const tunneled = [];
  const proxy = http.createServer((req, res) => {
    const url = new URL(req.url);
    proxied.push(url.pathname);
    const upstream = http.request({ host: '127.0.0.1', port, path: url.pathname + url.search, method: req.method, headers: req.headers }, answer => {
      res.writeHead(answer.statusCode, answer.headers); answer.pipe(res);
    });
    upstream.on('error', () => res.destroy()); req.pipe(upstream);
  });
  proxy.on('connect', (req, socket, head) => {
    tunneled.push(req.url);
    const upstream = net.connect(port, '127.0.0.1', () => {
      socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      if (head.length) upstream.write(head);
      socket.pipe(upstream); upstream.pipe(socket);
    });
    for (const connection of [socket, upstream]) {
      sockets.add(connection); connection.on('close', () => sockets.delete(connection)); connection.on('error', () => {});
    }
  });
  proxy.on('upgrade', (req, socket, head) => {
    const url = new URL(req.url);
    const upstream = net.connect(port, '127.0.0.1', () => {
      upstream.write(`${req.method} ${url.pathname} HTTP/1.1\r\n` + Object.entries(req.headers).map(([key, value]) => `${key}: ${value}\r\n`).join('') + '\r\n');
      if (head.length) upstream.write(head);
      socket.pipe(upstream); upstream.pipe(socket);
    });
    for (const connection of [socket, upstream]) {
      sockets.add(connection); connection.on('close', () => sockets.delete(connection)); connection.on('error', () => {});
    }
  });
  await listen(proxy);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'artex-recon-test-'));
  try {
    const config = path.join(dir, 'config.json');
    fs.writeFileSync(config, JSON.stringify({
      baseUrl: `http://artex-harvest.invalid:${port}/`, proxy: `http://127.0.0.1:${proxy.address().port}`,
      cookies: [{ name: 'fixture', value: 'local-test' }], localStorage: { fixture: 'loaded' },
      stubs: [{ match: '/api/menu', body: { code: 0, menu: [] } }],
      routes: ['/'], waitMs: 800, perRouteMs: 400, routeTimeout: 5000,
    }));
    const child = spawn(process.execPath, [path.join(__dirname, 'runtime_harvest.js'), config], {
      env: { ...process.env, HTTP_PROXY: '', HTTPS_PROXY: '', ALL_PROXY: '', NODE_EXTRA_CA_CERTS: '' },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let output = '';
    child.stdout.on('data', data => { output += data; }); child.stderr.on('data', data => { output += data; });
    const timer = setTimeout(() => child.kill(), 35000);
    const code = await new Promise((resolve, reject) => { child.on('error', reject); child.on('exit', resolve); });
    clearTimeout(timer);
    assert.equal(code, 0, output);
    const data = JSON.parse(fs.readFileSync(path.join(dir, 'runtime_api.json'), 'utf8'));
    assert.equal(data.evidenceKind, 'frontend_discovery_mock');
    assert.ok(data.rec.some(row => row.u === '/api/data' && row.responseSource === 'forwarded_modified' && row.actualStatus === 200 && row.resp.includes('401')), JSON.stringify(data.rec));
    assert.ok(data.rec.some(row => row.u === '/api/menu' && row.responseSource === 'configured_stub'));
    assert.ok(data.sse.includes('/api/events'));
    assert.ok(data.ws.some(row => row.dir === 'recv' && row.data === 'fixture-server'), JSON.stringify(data.ws));
    assert.ok(data.ws.some(row => row.dir === 'send' && row.data === 'fixture-client'));
    assert.ok(seen.some(row => row.cookie.includes('fixture=local-test') && row.url.includes('storage=loaded')));
    // Playwright's Node request client may CONNECT even for an HTTP URL.
    assert.ok(proxied.includes('/api/data') || tunneled.includes(`artex-harvest.invalid:${port}`), 'forwarded fetch did not reach the configured proxy');
    assert.ok(!seen.some(row => row.url.startsWith('/api/menu') || row.url.startsWith('/api/events')), 'stub contacted target');
  } finally {
    for (const socket of sockets) socket.destroy();
    await Promise.all([close(proxy), close(fixture)]);
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
