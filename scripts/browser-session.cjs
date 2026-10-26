#!/usr/bin/env node
// Private stdio bridge. CDP listens on loopback and is never published by Compose.
const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline');
const { prepareBrowserHome } = require('./browser-runtime.cjs');

async function main() {
  const { chromium } = require('playwright');
  const options = JSON.parse(process.env.ARTEX_BROWSER_OPTIONS || '{}');
  const root = options.directory;
  if (!root || !Number.isInteger(options.port)) throw new Error('브라우저 실행 설정이 없습니다');
  fs.mkdirSync(root, { recursive: true, mode: 0o700 });
  const home = path.join(root, 'home');
  prepareBrowserHome(home);
  const sessionFile = path.join(root, 'session.json');
  let saved = { pages: [] };
  try { saved = JSON.parse(fs.readFileSync(sessionFile, 'utf8')); } catch {}
  const launch = {
    headless: true,
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || process.env.CHROMIUM || undefined,
    viewport: { width: 1280, height: 800 },
    env: { ...process.env, HOME: home },
    // The existing image runs as root. Chromium's setuid sandbox cannot run as root.
    chromiumSandbox: typeof process.getuid !== 'function' || process.getuid() !== 0,
    args: [`--remote-debugging-port=${options.port}`, '--remote-debugging-address=127.0.0.1'],
  };
  if (options.proxy) {
    const proxy = new URL(options.proxy);
    launch.proxy = {
      server: `${proxy.protocol}//${proxy.host}`,
      username: proxy.username ? decodeURIComponent(proxy.username) : undefined,
      password: proxy.password ? decodeURIComponent(proxy.password) : undefined,
      bypass: options.bypass || '<-loopback>',
    };
  }
  const context = await chromium.launchPersistentContext(path.join(root, 'profile'), launch);
  context.setDefaultTimeout(10000);
  // Session cookies are not reliably persisted by Chromium across process exits.
  if (Array.isArray(saved.cookies) && saved.cookies.length) await context.addCookies(saved.cookies);
  let current = context.pages()[0] || await context.newPage();
  // Restore navigation only for the explicitly resumed session, never another task's profile.
  const previous = (saved.pages || []).find((p) => /^https?:\/\//.test(p.url));
  if (previous) {
    const cdp = await context.newCDPSession(current);
    await cdp.send('Page.enable');
    const { identifier } = await cdp.send('Page.addScriptToEvaluateOnNewDocument', {
      source: `(() => { const p = ${JSON.stringify(previous)}; if (p.origin === location.origin) for (const [k,v] of Object.entries(p.storage || {})) sessionStorage.setItem(k,v); })();`,
    });
    await current.goto(previous.url, { waitUntil: 'domcontentloaded', timeout: 15000 }).catch(() => {});
    // Restore once. Later logouts or token rotations must not resurrect the old values.
    await cdp.send('Page.removeScriptToEvaluateOnNewDocument', { identifier });
    await cdp.detach();
  }
  context.on('page', (page) => { current = page; });

  const page = async () => {
    if (current.isClosed()) current = context.pages().at(-1) || await context.newPage();
    return current;
  };
  async function status() {
    const p = await page();
    const tabs = await Promise.all(context.pages().map(async (tab, index) => ({
      index, title: await tab.title().catch(() => ''), url: tab.url(), selected: tab === p,
    })));
    return { url: p.url(), title: await p.title().catch(() => ''), viewport: p.viewportSize() || { width: 1280, height: 800 }, tabs };
  }
  async function screenshot(p) {
    const deadline = Date.now() + 10000;
    // A navigation can strand Chromium's capture against the previous document.
    // Retry only that observation, never the user's click or submitted form.
    for (let attempt = 0; attempt < 2; attempt++) {
      const navigated = Symbol('navigated');
      let onNavigation;
      const navigation = new Promise((resolve) => {
        onNavigation = (frame) => { if (frame === p.mainFrame()) resolve(navigated); };
        p.on('framenavigated', onNavigation);
      });
      let cdp, timer;
      try {
        cdp = await context.newCDPSession(p);
        const capture = cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }).catch((error) => {
          // Chromium may detach the old document before Playwright reports its navigation.
          if (!p.isClosed() && error.message.includes('Not attached to an active page')) return navigation;
          throw error;
        });
        const timeout = new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error('브라우저 화면 응답 시간이 초과되었습니다')), Math.max(1, deadline - Date.now()));
        });
        const result = await Promise.race([capture, navigation, timeout]);
        if (result !== navigated) return result.data;
      } finally {
        p.off('framenavigated', onNavigation);
        clearTimeout(timer);
        if (cdp) await cdp.detach().catch(() => {});
      }
      await p.waitForLoadState('domcontentloaded', { timeout: Math.max(1, deadline - Date.now()) });
    }
    throw new Error('페이지가 이동 중입니다');
  }
  async function save() {
    const pages = await Promise.all(context.pages().map(async (p) => {
      const state = await p.evaluate(() => ({ origin: location.origin, storage: { ...sessionStorage } })).catch(() => ({}));
      return { url: p.url(), ...state };
    }));
    const cookies = await context.cookies();
    fs.writeFileSync(`${sessionFile}.tmp`, JSON.stringify({ pages, cookies }), { mode: 0o600 });
    fs.renameSync(`${sessionFile}.tmp`, sessionFile);
  }
  let closing = false;
  async function close() {
    if (closing) return;
    closing = true;
    await save().catch(() => {});
    await context.close().catch(() => {});
    process.exit(0);
  }
  process.on('SIGTERM', close);
  process.on('SIGINT', close);
  const input = readline.createInterface({ input: process.stdin });
  input.on('close', close);
  let queue = Promise.resolve();
  input.on('line', (line) => {
    queue = queue.then(async () => {
      let request;
      try {
        request = JSON.parse(line);
        const p = await page();
        let result;
        switch (request.action) {
          case 'status': result = await status(); break;
          case 'screenshot': result = { image: await screenshot(p) }; break;
          case 'click': await p.mouse.click(request.x, request.y); break;
          case 'drag':
            await p.mouse.move(request.x, request.y);
            await p.mouse.down();
            try { await p.mouse.move(request.to_x, request.to_y, { steps: 12 }); }
            finally { await p.mouse.up(); }
            break;
          case 'type': await p.keyboard.insertText(request.text); break;
          case 'key': await p.keyboard.press(request.key); break;
          case 'scroll': await p.mouse.wheel(request.x || 0, request.y || 0); break;
          case 'navigate': await p.goto(request.url, { waitUntil: 'domcontentloaded', timeout: 20000 }); break;
          case 'select_tab': {
            const selected = context.pages()[request.index];
            if (!selected) throw new Error('탭을 찾을 수 없습니다');
            current = selected;
            await current.bringToFront();
            break;
          }
          default: throw new Error('지원하지 않는 브라우저 동작입니다');
        }
        if (!result) result = await status();
        process.stdout.write(`${JSON.stringify({ id: request.id, result })}\n`);
      } catch {
        // Browser errors may contain a typed secret or token-bearing URL. Keep them out of logs.
        process.stdout.write(`${JSON.stringify({ id: request?.id, error: '브라우저 동작을 완료하지 못했습니다. 페이지와 선택한 탭을 확인해 주세요.' })}\n`);
      }
    });
  });
  process.stdout.write(`${JSON.stringify({ ready: true })}\n`);
}
if (require.main === module) main().catch(() => {
  process.stderr.write('브라우저 시작 실패: 설치된 Chromium, 프로필 잠금 및 프록시 CA 설정을 확인해 주세요.\n');
  process.exit(1);
});
