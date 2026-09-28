// Isolated, synthetic browser diagnostic. Only loopback UDP/HTTP traffic is used.
import { chromium } from 'playwright';
import { createServer } from 'node:http';
import { createSocket } from 'node:dgram';
import { diagnosticVerdict, safeExceptionName } from './preview-diagnostic-policy.mjs';

const scenarios = ['control', 'policy-top', 'policy-data', 'policy-srcdoc', 'policy-blob-srcdoc'];
const httpRequests = new Map();
const server = createServer((request, response) => {
  response.setHeader('Cache-Control', 'no-store');
  if (request.url.startsWith('/http-probe/')) {
    httpRequests.set(request.url, (httpRequests.get(request.url) || 0) + 1);
    response.setHeader('Access-Control-Allow-Origin', '*');
    response.setHeader('Content-Type', 'text/plain');
    response.end('synthetic-loopback-response');
    return;
  }
  response.setHeader('Content-Type', 'text/html');
  if (request.url !== '/control') {
    response.setHeader('Connection-Allowlist', '(response-origin);webrtc=block');
    response.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'unsafe-inline'; frame-src data: blob:; connect-src 'none'");
  }
  response.end('<!doctype html><title>Isolated synthetic network diagnostic</title>');
});

function createProbe(protocol, target) {
  const operation = protocol === 'rtc' ? `
    let peer;
    try {
      tell('constructor');
      peer = new RTCPeerConnection({iceServers:[{urls:${JSON.stringify(target)}}]});
      tell('channel'); peer.createDataChannel('synthetic');
      tell('offer'); const offer = await peer.createOffer();
      tell('description');
      await Promise.race([
        peer.setLocalDescription(offer),
        new Promise((_, reject) => setTimeout(() => reject(new DOMException('', 'TimeoutError')), 1000)),
      ]);
      tell('waiting');
      await new Promise(resolve => setTimeout(resolve, 1500));
      tell('completed');
    } finally { if (peer) peer.close(); }
  ` : `
    const target = ${JSON.stringify(target)};
    document.addEventListener('securitypolicyviolation', event => {
      if (event.effectiveDirective === 'connect-src' &&
          (event.blockedURI === target || event.blockedURI === new URL(target).origin)) {
        connectBlocked = true;
        tell(stage, exceptionName);
      }
    });
    tell('fetch');
    const response = await fetch(target, {
      credentials: 'omit', cache: 'no-store', redirect: 'error', signal: AbortSignal.timeout(2500),
    });
    if (!response.ok || await response.text() !== 'synthetic-loopback-response') throw new Error();
    tell('completed');
  `;
  return `<script>(async () => {
    const exceptionType = ${safeExceptionName.toString()};
    let stage = 'starting', exceptionName = null, connectBlocked = false;
    const tell = (nextStage, nextException = null) => {
      stage = nextStage; exceptionName = nextException;
      top.postMessage({ diagnostic: { stage, exceptionName, connectBlocked } }, '*');
    };
    try { ${operation} }
    catch (error) { tell('rejected', exceptionType(error)); }
  })();<\/script>`;
}

await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
try {
  browser = await chromium.launch({ channel: process.env.OSANWE_BROWSER_CHANNEL || 'chrome', headless: true });
  for (const protocol of ['http', 'rtc']) {
    let calibrated = false;
    for (const scenario of scenarios) {
      // Separate UDP sockets prevent a late packet from a preceding scenario
      // being attributed to the next one. HTTP probes have distinct paths.
      let udp;
      let packets = 0;
      let page;
      const probePath = '/http-probe/' + protocol + '/' + scenario;
      const result = { browser: browser.version(), protocol, scenario, stage: 'starting', crashed: false, exceptionName: null, connectBlocked: false };
      try {
        let target = `http://127.0.0.1:${server.address().port}${probePath}`;
        if (protocol === 'rtc') {
          udp = createSocket('udp4');
          udp.on('message', () => packets++);
          await new Promise(resolve => udp.bind(0, '127.0.0.1', resolve));
          target = `stun:127.0.0.1:${udp.address().port}`;
        }
        const probe = createProbe(protocol, target);
        page = await browser.newPage();
        page.on('console', message => {
          if (!message.text().startsWith('diagnostic:')) return;
          try {
            const observed = JSON.parse(message.text().slice(11));
            if (['starting', 'constructor', 'channel', 'offer', 'description', 'waiting', 'fetch', 'completed', 'rejected'].includes(observed.stage)) {
              result.stage = observed.stage;
              result.exceptionName = observed.exceptionName === null ? null : safeExceptionName({ name: observed.exceptionName });
              result.connectBlocked = observed.connectBlocked === true;
            }
          } catch { /* Invalid telemetry cannot satisfy the completion gate. */ }
        });
        page.on('crash', () => { result.crashed = true; });
        await page.goto(`http://127.0.0.1:${server.address().port}/${scenario === 'control' ? 'control' : 'policy'}`);
        await page.evaluate(({ probe, scenario }) => {
          window.addEventListener('message', event => {
            if (event.data?.diagnostic) console.log('diagnostic:' + JSON.stringify(event.data.diagnostic));
          });
          if (scenario === 'control' || scenario === 'policy-top') {
            const script = document.createElement('script');
            script.textContent = probe.slice(8, -9);
            document.body.appendChild(script);
          } else {
            const frame = document.createElement('iframe');
            frame.setAttribute('sandbox', 'allow-scripts');
            let source = probe;
            if (scenario.endsWith('srcdoc')) {
              source = '<script>const child = document.createElement("iframe"); child.srcdoc = ' +
                JSON.stringify(probe).replaceAll('</script>', '<\\/script>') + '; document.body.appendChild(child);<\/script>';
            }
            frame.src = scenario.includes('blob')
              ? URL.createObjectURL(new Blob(['<!doctype html><body>' + source], { type: 'text/html' }))
              : 'data:text/html;base64,' + btoa('<!doctype html><body>' + source);
            document.body.appendChild(frame);
          }
        }, { probe, scenario });
        await new Promise(resolve => setTimeout(resolve, 3500));
      } catch (error) {
        result.interrupted = true;
        result.exceptionName = safeExceptionName(error);
      } finally {
        if (page) await page.close().catch(() => {});
        if (udp) await new Promise(resolve => udp.close(resolve));
      }
      result.packets = packets;
      result.requests = httpRequests.get(probePath) || 0;
      const verdict = diagnosticVerdict(result, calibrated);
      if (scenario === 'control') calibrated = verdict.passed;
      console.log(JSON.stringify({ ...result, calibrated, ...verdict }));
      if (!verdict.passed) process.exitCode = 1;
    }
  }
} catch (error) {
  console.log(JSON.stringify({ outcome: 'incomplete', passed: false, reason: 'diagnostic-interrupted', exceptionName: safeExceptionName(error) }));
  process.exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  await new Promise(resolve => server.close(resolve));
}
