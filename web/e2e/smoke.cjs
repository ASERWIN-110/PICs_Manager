const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const WebSocket = require('ws');

const FRONTEND_URL = process.env.E2E_FRONTEND_URL || 'http://127.0.0.1:5173/';
const CHROME_BIN = process.env.CHROME_BIN || '/usr/bin/google-chrome';
const DEBUG_PORT = Number(process.env.E2E_CHROME_PORT || 9333);
const DEBUG_URL = `http://127.0.0.1:${DEBUG_PORT}`;
const IMAGE_SEARCH_FILE = process.env.E2E_IMAGE_SEARCH_FILE || path.resolve(__dirname, '../../Pictures/0307_142002427_p1.jpg');

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function fetchJSON(url) {
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`GET ${url} returned ${res.status}`);
  }
  return res.json();
}

async function createPageTarget(url) {
  const res = await fetch(`${DEBUG_URL}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' });
  if (!res.ok) {
    throw new Error(`PUT /json/new returned ${res.status}`);
  }
  return res.json();
}

async function waitForChrome() {
  const deadline = Date.now() + 10000;
  let lastError;
  while (Date.now() < deadline) {
    try {
      return await fetchJSON(`${DEBUG_URL}/json/version`);
    } catch (err) {
      lastError = err;
      await sleep(200);
    }
  }
  throw new Error(`Chrome DevTools endpoint did not start: ${lastError?.message || 'timeout'}`);
}

function connect(wsURL) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(wsURL);
    ws.once('open', () => resolve(ws));
    ws.once('error', reject);
  });
}

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.nextId = 1;
    this.pending = new Map();
    this.consoleErrors = [];
    this.exceptions = [];
    ws.on('message', raw => {
      const msg = JSON.parse(String(raw));
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(`${msg.error.message}: ${msg.error.data || ''}`));
        else resolve(msg.result);
        return;
      }
      if (msg.method === 'Runtime.exceptionThrown') {
        this.exceptions.push(msg.params.exceptionDetails?.text || 'Runtime exception');
      }
      if (msg.method === 'Runtime.consoleAPICalled' && ['error', 'assert'].includes(msg.params.type)) {
        this.consoleErrors.push(msg.params.args?.map(arg => arg.value || arg.description).join(' ') || msg.params.type);
      }
      if (msg.method === 'Log.entryAdded' && msg.params.entry?.level === 'error') {
        this.consoleErrors.push(msg.params.entry.text);
      }
    });
  }

  send(method, params = {}) {
    const id = this.nextId++;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }

  async eval(expression) {
    const result = await this.send('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
      userGesture: true,
    });
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.text || 'Evaluation failed');
    }
    return result.result.value;
  }

  async querySelector(selector) {
    const documentResult = await this.send('DOM.getDocument', { depth: 1 });
    return this.send('DOM.querySelector', {
      nodeId: documentResult.root.nodeId,
      selector,
    });
  }
}

function asExpression(fn, ...args) {
  return `(${fn})(${args.map(arg => JSON.stringify(arg)).join(',')})`;
}

async function waitFor(cdp, predicate, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await cdp.eval(asExpression(predicate));
    if (value) return value;
    await sleep(250);
  }
  throw new Error(`Timed out waiting for condition: ${predicate.toString().slice(0, 120)}`);
}

async function pageDiagnostics(cdp) {
  return cdp.eval(asExpression(() => ({
    url: location.href,
    readyState: document.readyState,
    title: document.title,
    text: document.body?.innerText?.slice(0, 1200) || '',
    html: document.body?.innerHTML?.slice(0, 1200) || '',
  })));
}

async function stopChrome(chrome) {
  if (!chrome || chrome.killed) return;
  const exited = new Promise(resolve => chrome.once('exit', resolve));
  chrome.kill('SIGTERM');
  await Promise.race([exited, sleep(3000)]);
  if (!chrome.killed) {
    chrome.kill('SIGKILL');
    await Promise.race([exited, sleep(1000)]);
  }
}

async function removeProfile(profileDir) {
  for (let attempt = 0; attempt < 5; attempt++) {
    try {
      fs.rmSync(profileDir, { recursive: true, force: true });
      return;
    } catch (err) {
      if (attempt === 4) throw err;
      await sleep(250);
    }
  }
}

async function main() {
  if (!fs.existsSync(CHROME_BIN)) {
    throw new Error(`Chrome binary not found: ${CHROME_BIN}`);
  }
  const profileDir = fs.mkdtempSync(path.join(os.tmpdir(), 'pics-manager-chrome-'));
  const chrome = spawn(CHROME_BIN, [
    '--headless=new',
    '--disable-gpu',
    '--no-sandbox',
    `--remote-debugging-port=${DEBUG_PORT}`,
    `--user-data-dir=${profileDir}`,
    'about:blank',
  ], { stdio: ['ignore', 'pipe', 'pipe'] });

  let cdp;
  try {
    await waitForChrome();
    const page = await createPageTarget(FRONTEND_URL);
    cdp = new CDP(await connect(page.webSocketDebuggerUrl));
    await cdp.send('Runtime.enable');
    await cdp.send('Page.enable');
    await cdp.send('Log.enable');
    await cdp.send('Page.navigate', { url: FRONTEND_URL });

    await waitFor(cdp, () => document.readyState === 'complete');
    try {
      await waitFor(cdp, () => !document.querySelector('.error-banner') && (
        document.querySelectorAll('.series-card').length > 0 ||
        document.body.textContent.includes('没有匹配的系列。')
      ), 30000);
    } catch (err) {
      const diagnostics = await pageDiagnostics(cdp);
      throw new Error(`${err.message}\nPage diagnostics: ${JSON.stringify(diagnostics, null, 2)}\nBrowser errors: ${JSON.stringify({ exceptions: cdp.exceptions, consoleErrors: cdp.consoleErrors }, null, 2)}`);
    }

    const home = await cdp.eval(asExpression(() => {
      const cards = [...document.querySelectorAll('.series-card')];
      const total = document.querySelector('.metric-strip strong')?.textContent?.trim();
      const thumbs = [...document.querySelectorAll('.series-cover img')].map(img => ({
        src: img.getAttribute('src'),
        width: img.naturalWidth,
        loading: img.getAttribute('loading'),
      }));
      return { cardCount: cards.length, total, thumbs };
    }));
    if (!/^\d+$/.test(home.total || '')) {
      throw new Error(`Series total is not numeric: ${home.total}`);
    }
    if (home.thumbs.some(thumb => !thumb.src?.includes('/thumbnail') || thumb.loading !== 'lazy')) {
      throw new Error(`Series thumbnails are not lazy URL thumbnails: ${JSON.stringify(home.thumbs.slice(0, 3))}`);
    }

    let page2 = null;
    let media = null;
    let textSearch = null;
    let imageSearch = null;

    if (home.cardCount > 0) {
      const nextEnabled = await cdp.eval(asExpression(() => {
        const button = [...document.querySelectorAll('.pagination .button')].find(item => item.textContent.includes('下一页'));
        return Boolean(button && !button.disabled);
      }));
      if (nextEnabled) {
        await cdp.eval(asExpression(() => [...document.querySelectorAll('.pagination .button')].find(button => button.textContent.includes('下一页'))?.click()));
        await waitFor(cdp, () => document.querySelector('.metric-strip div:nth-child(2) strong')?.textContent?.trim() === '2', 15000);
        page2 = await cdp.eval(asExpression(() => ({
          page: document.querySelector('.metric-strip div:nth-child(2) strong')?.textContent?.trim(),
          cards: document.querySelectorAll('.series-card').length,
          names: [...document.querySelectorAll('.series-name')].slice(0, 3).map(el => el.textContent),
        })));
        if (page2.page !== '2' || page2.cards < 1) {
          throw new Error(`Cursor pagination did not reach page 2: ${JSON.stringify(page2)}`);
        }
        await cdp.eval(asExpression(() => [...document.querySelectorAll('.pagination .button')].find(button => button.textContent.includes('上一页'))?.click()));
        await waitFor(cdp, () => document.querySelector('.metric-strip div:nth-child(2) strong')?.textContent?.trim() === '1', 15000);
      }

      await cdp.eval(asExpression(() => document.querySelector('.series-card')?.click()));
      await waitFor(cdp, () => document.querySelector('.media-tabs') && !document.querySelector('.inline-state.error') && !document.body.textContent.includes('正在加载') && (
        document.querySelectorAll('.media-tile').length > 0 ||
        document.body.textContent.includes('这个系列还没有')
      ), 15000);
      media = await cdp.eval(asExpression(() => {
        const tabs = [...document.querySelectorAll('.media-tab')].map(tab => tab.textContent.trim());
        const active = document.querySelector('.media-tab.active')?.textContent?.trim() || '';
        const tiles = [...document.querySelectorAll('.media-tile')];
        const thumbs = [...document.querySelectorAll('.media-thumb')].map(img => ({
          src: img.getAttribute('src'),
          width: img.naturalWidth,
          loading: img.getAttribute('loading'),
        }));
        return { tabs, active, tileCount: tiles.length, thumbs };
      }));
      if (!media.tabs.includes('图片') || !media.tabs.includes('视频')) {
        throw new Error(`Media type tabs were not rendered: ${JSON.stringify(media.tabs)}`);
      }
      if (media.active !== '图片') {
        throw new Error(`Expected image tab to be active by default: ${media.active}`);
      }
      if (media.tileCount < 1) {
        throw new Error(`Expected at least one media tile for the first series: ${JSON.stringify(media)}`);
      }
      if (media.thumbs.length && media.thumbs.some(thumb => !thumb.src?.includes('/thumbnail') || thumb.loading !== 'lazy')) {
        throw new Error(`Media thumbnails are not lazy URL thumbnails: ${JSON.stringify(media.thumbs.slice(0, 3))}`);
      }
      await cdp.eval(asExpression(() => [...document.querySelectorAll('.media-tab')].find(tab => tab.textContent.trim() === '视频')?.click()));
      await waitFor(cdp, () => document.querySelector('.media-tab.active')?.textContent?.trim() === '视频' && !document.querySelector('.inline-state.error'), 15000);

      const firstSeriesName = await cdp.eval(asExpression(() => document.querySelector('.series-name')?.textContent?.trim() || ''));
      if (firstSeriesName) {
        await cdp.eval(asExpression((query) => {
          const input = document.querySelector('.search-input');
          const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
          setter.call(input, query);
          input.dispatchEvent(new Event('input', { bubbles: true }));
          document.querySelector('.search-bar').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        }, firstSeriesName));
        try {
          await waitFor(cdp, () => document.body.textContent.includes('当前筛选') && document.querySelectorAll('.series-card').length > 0, 15000);
        } catch (err) {
          const diagnostics = await pageDiagnostics(cdp);
          throw new Error(`${err.message}\nSearch diagnostics: ${JSON.stringify(diagnostics, null, 2)}\nBrowser errors: ${JSON.stringify({ exceptions: cdp.exceptions, consoleErrors: cdp.consoleErrors }, null, 2)}`);
        }
        textSearch = await cdp.eval(asExpression(() => ({
          filter: document.querySelector('.query-banner')?.textContent || '',
          names: [...document.querySelectorAll('.series-name')].map(el => el.textContent),
        })));
      }

      if (fs.existsSync(IMAGE_SEARCH_FILE)) {
        await cdp.eval(asExpression(() => document.querySelector('.query-banner .link-button')?.click()));
        await waitFor(cdp, () => !document.querySelector('.query-banner'), 15000);
        const fileInput = await cdp.querySelector('input[type="file"]');
        if (!fileInput.nodeId) {
          throw new Error('Could not find image search file input');
        }
        await cdp.send('DOM.setFileInputFiles', {
          nodeId: fileInput.nodeId,
          files: [IMAGE_SEARCH_FILE],
        });
        await cdp.eval(asExpression(() => {
          const input = document.querySelector('input[type="file"]');
          input.dispatchEvent(new Event('input', { bubbles: true }));
          input.dispatchEvent(new Event('change', { bubbles: true }));
        }));
        await waitFor(cdp, () => document.querySelector('.query-banner')?.textContent.includes('以图搜图'), 30000);
        imageSearch = await cdp.eval(asExpression(() => ({
          filter: document.querySelector('.query-banner')?.textContent || '',
          names: [...document.querySelectorAll('.series-name')].map(el => el.textContent),
        })));
        if (!imageSearch.filter.includes('以图搜图')) {
          throw new Error(`Image search did not enter image-search mode: ${JSON.stringify(imageSearch)}`);
        }
      }
    }

    await cdp.send('Page.navigate', { url: new URL('/admin', FRONTEND_URL).toString() });
    await waitFor(cdp, () => document.querySelector('.admin-page') && !document.querySelector('.error-banner'), 15000);
    const admin = await cdp.eval(asExpression(() => {
      const uri = document.querySelector('input[name="database.uri"]')?.value || '';
      const modeOptions = [...document.querySelectorAll('.scan-row select option')].map(option => option.value);
      const mediaTypes = document.querySelector('.rule-list')?.textContent || '';
      return { uri, modeOptions, mediaTypes };
    }));
    if (!admin.uri.includes('xxxxx') || admin.uri.includes('secret')) {
      throw new Error(`Database URI is not redacted in admin UI: ${admin.uri}`);
    }
    if (!admin.modeOptions.includes('full') || !admin.modeOptions.includes('classifyOnly')) {
      throw new Error(`Scan mode options missing: ${admin.modeOptions.join(',')}`);
    }
    if (!admin.mediaTypes.includes('image')) {
      throw new Error(`Media type rules were not rendered: ${admin.mediaTypes}`);
    }

    if (cdp.exceptions.length || cdp.consoleErrors.length) {
      throw new Error(`Browser errors: ${JSON.stringify({ exceptions: cdp.exceptions, consoleErrors: cdp.consoleErrors })}`);
    }

    console.log(JSON.stringify({ ok: true, home, page2, media, textSearch, imageSearch, admin }, null, 2));
  } finally {
    if (cdp?.ws) cdp.ws.close();
    await stopChrome(chrome);
    await removeProfile(profileDir);
  }
}

main().catch(err => {
  console.error(err);
  process.exit(1);
});
