// Takes the wiki screenshots from a running demo instance (see wiki/Development-and-Testing.md).
// Usage: node scripts/screenshots.js <output-dir> [base-url]
// Needs Edge or Chrome; set BROWSER to its path if it is not in a standard place.
// ONLY point this at demo data: the pages are clipped so the Settings page footer (which shows the
// database path of your computer) is never included, but nothing else is filtered.
const { spawn } = require('child_process'), fs = require('fs'), path = require('path'), os = require('os');
const candidates = [process.env.BROWSER, "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe", "C:/Program Files/Google/Chrome/Application/chrome.exe", "/usr/bin/google-chrome", "/usr/bin/chromium", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"];
const edge = candidates.find(p => p && fs.existsSync(p));
if (!edge) throw new Error("no Edge/Chrome found; set BROWSER");
const out = process.argv[2], base = process.argv[3] || 'http://127.0.0.1:8765';
if (!out) throw new Error("usage: node scripts/screenshots.js <output-dir> [base-url]");
fs.mkdirSync(out, { recursive: true });
const sleep = ms => new Promise(r => setTimeout(r, ms));
(async () => {
  const proc = spawn(edge, ['--headless=new', '--remote-debugging-port=9333', '--user-data-dir=' + path.join(os.tmpdir(), 'bb-shots-profile'), '--hide-scrollbars', '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
  let targets;
  for (let i = 0; i < 40; i++) { try { targets = await (await fetch('http://127.0.0.1:9333/json')).json(); if (targets.length) break; } catch (e) {} await sleep(300); }
  const page = targets.find(t => t.type === 'page');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise(r => ws.onopen = r);
  let id = 0; const pending = {};
  ws.onmessage = m => { const d = JSON.parse(m.data); if (d.id && pending[d.id]) { pending[d.id](d); delete pending[d.id]; } };
  const send = (method, params = {}) => new Promise(r => { const i = ++id; pending[i] = r; ws.send(JSON.stringify({ id: i, method, params })); });
  const ev = async expr => (await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })).result.result.value;
  async function go(url, theme) {
    await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: theme }] });
    await send('Page.navigate', { url: base + url });
    await sleep(1800);
  }
  async function shot(name, maxH = 2200) {
    const h = Math.min(await ev("document.documentElement.scrollHeight"), maxH);
    await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: h, deviceScaleFactor: 1, mobile: false });
    await sleep(1800); // let charts finish drawing at the final size
    const r = await send("Page.captureScreenshot", { format: "png" });
    fs.writeFileSync(path.join(out, name + ".png"), Buffer.from(r.result.data, "base64"));
    await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
    console.log("saved", name);
  }
  await send('Page.enable'); await send('Runtime.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  for (const theme of ['light', 'dark']) {
    await go('/?months=12', theme); await shot('dashboard-' + theme);
  }
  await go('/transactions', 'light'); await shot('transactions', 1300);
  await go('/transactions', 'light');
  await ev("document.getElementById('ftoggle').click()"); await sleep(300); await shot('transactions-filters', 1100);
  await go('/transactions?confidence=unknown', 'light');
  await ev("document.querySelector('.suggest-btn').click()"); await sleep(1500); await shot('suggestions', 640);
  await go('/charts', 'light'); await sleep(800); await shot('charts');
  await go('/mortgage', 'light'); await sleep(2500); await shot('mortgage');
  await go('/report', 'light'); await shot('lender-view');
  await go('/import', 'light'); await shot('import');
  await go('/settings', 'light'); await shot('settings', 2300);
  await go('/transactions?reimb=only', 'light'); await shot('repayments-from-others', 1100);
  await go('/?months=12', 'dark'); // mobile
  await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
  await go('/?months=12', 'light'); await sleep(500);
  const r = await send('Page.captureScreenshot', { format: 'png' });
  fs.writeFileSync(path.join(out, 'mobile-dashboard.png'), Buffer.from(r.result.data, 'base64')); console.log('saved mobile-dashboard');
  ws.close(); proc.kill();
  process.exit(0);
})().catch(e => { console.error(e); process.exit(1); });
