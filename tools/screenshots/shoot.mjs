// Screenshot driver for the README images.
//
// Chrome DevTools Protocol over Node's global WebSocket, with no dependencies —
// this repo has one Go dependency and no frontend build step, and a screenshot
// tool is not the thing to add a package tree for.
//
// Screenshots are taken against the real app: real assets, real CSS, the real
// decoder. Only the *data* is substituted, because a published image of a live
// mesh would carry the network's identity and every device's address. A scene
// file says which view to show and what to put in it.
//
// Usage (see capture.sh, which drives both scenes):
//   SCENE=scenes/channels.js OUT=docs/channel-noise.png HEIGHT=745 node shoot.mjs
//
// Environment:
//   SCENE   scene file to evaluate in the page (required)
//   OUT     PNG path to write (required)
//   BASE    running OTBR Insight instance (default http://127.0.0.1:8099)
//   WIDTH   CSS viewport width  (default 1440)
//   HEIGHT  CSS viewport height (default 900)
//   FULL    "1" grows the viewport to the whole page instead of cropping
//   FIXTURE file whose contents replace __FIXTURE__ in the scene
import { readFileSync, writeFileSync } from 'node:fs';

const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const DEBUGGER = process.env.DEBUGGER || 'http://127.0.0.1:9222';
const width = Number(process.env.WIDTH || 1440);
let height = Number(process.env.HEIGHT || 900);

if (!process.env.SCENE || !process.env.OUT) {
  console.error('SCENE and OUT are required');
  process.exit(2);
}

// /json/new only accepts PUT; a GET is refused with a plain-text explanation
// that is not JSON, which is a confusing way to discover the requirement.
const target = await (await fetch(
  `${DEBUGGER}/json/new?${encodeURIComponent(BASE + '/')}`, { method: 'PUT' }
)).json();

const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve) => ws.addEventListener('open', resolve));

let sequence = 0;
const pending = new Map();
ws.addEventListener('message', (event) => {
  const message = JSON.parse(event.data);
  if (message.id && pending.has(message.id)) {
    pending.get(message.id)(message);
    pending.delete(message.id);
  }
});

const send = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++sequence;
  pending.set(id, (message) => message.error
    ? reject(new Error(`${method}: ${JSON.stringify(message.error)}`))
    : resolve(message.result));
  ws.send(JSON.stringify({ id, method, params }));
});

const evaluate = async (expression) => {
  const result = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
  return result.result.value;
};

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const viewport = () => send('Emulation.setDeviceMetricsOverride',
  { width, height, deviceScaleFactor: 2, mobile: false });

await send('Page.enable');
await send('Runtime.enable');
await viewport();
await send('Page.navigate', { url: BASE + '/' });

// Wait for the first poll rather than sleeping a guessed interval: the sidebar
// status pill reads "Connecting" until then, and a screenshot that catches it
// mid-connect looks like a broken instance.
for (let attempt = 0; attempt < 40; attempt++) {
  await sleep(250);
  const status = await evaluate(`document.querySelector('[data-status-label]')?.textContent || ''`);
  if (status && status !== 'Connecting') break;
}

// The scene may need a data file; it asks for it by embedding __FIXTURE__,
// which is replaced with the file's contents as a JSON string literal.
let scene = readFileSync(process.env.SCENE, 'utf8');
if (scene.includes('__FIXTURE__')) {
  scene = scene.replace('__FIXTURE__', () => JSON.stringify(readFileSync(process.env.FIXTURE, 'utf8')));
}
await evaluate(scene);
await sleep(1200);

if (process.env.FULL === '1') {
  height = Math.min(await evaluate('Math.ceil(document.documentElement.scrollHeight)'), 4000);
  await viewport();
  await sleep(600);
}

const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: process.env.FULL === '1' });
writeFileSync(process.env.OUT, Buffer.from(shot.data, 'base64'));
console.log(`wrote ${process.env.OUT} (${width}x${height} CSS px)`);
ws.close();
process.exit(0);
