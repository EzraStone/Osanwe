// Isolated, synthetic browser diagnostic. Only loopback UDP/HTTP traffic is used.
import { chromium } from 'playwright';
import { createServer } from 'node:http';
import { createSocket } from 'node:dgram';

const udp = createSocket('udp4');
let packets = 0;
udp.on('message', () => packets++);
await new Promise(resolve => udp.bind(0, '127.0.0.1', resolve));
const probe = `<script>(async () => {
  const tell = stage => top.postMessage({ diagnostic: stage }, '*');
  let peer;
  try {
    tell('constructor');
    peer = new RTCPeerConnection({iceServers:[{urls:'stun:127.0.0.1:${udp.address().port}'}]});
    tell('channel'); peer.createDataChannel('synthetic');
    tell('offer'); const offer = await peer.createOffer();
    tell('description');
    await Promise.race([peer.setLocalDescription(offer), new Promise(resolve => setTimeout(resolve, 1000))]);
    tell('waiting');
    await new Promise(resolve => setTimeout(resolve, 1500));
    tell('completed');
  } catch { tell('rejected'); }
  finally { if (peer) peer.close(); }
})();<\/script>`;
const server = createServer((request, response) => {
  response.setHeader('Content-Type', 'text/html');
  if (request.url !== '/control') {
    response.setHeader('Connection-Allowlist', '(response-origin);webrtc=block');
    response.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'unsafe-inline'; frame-src data: blob:; connect-src 'none'");
  }
  response.end('<!doctype html><title>Isolated synthetic RTC diagnostic</title>');
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const browser = await chromium.launch({ channel: process.env.OSANWE_BROWSER_CHANNEL || 'chrome', headless: true });
try {
  for (const scenario of ['control', 'policy-top', 'policy-data', 'policy-srcdoc', 'policy-blob-srcdoc']) {
    packets = 0;
    const page = await browser.newPage();
    let stage = 'starting';
    page.on('console', msg => { if (msg.text().startsWith('diagnostic:')) stage = msg.text().slice(11); });
    let crashed = false;
    page.on('crash', () => { crashed = true; });
    try {
      await page.goto(`http://127.0.0.1:${server.address().port}/${scenario === 'control' ? 'control' : 'policy'}`);
      await page.evaluate(({ probe, scenario }) => {
        window.addEventListener('message', event => { if (event.data?.diagnostic) console.log('diagnostic:' + event.data.diagnostic); });
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
      console.log(JSON.stringify({ browser: browser.version(), scenario, stage, crashed, packets }));
      if (scenario !== 'control' && (crashed || packets || !['completed', 'rejected'].includes(stage))) process.exitCode = 1;
    } catch {
      console.log(JSON.stringify({ browser: browser.version(), scenario, stage, crashed, packets, interrupted: true }));
      process.exitCode = 1;
    } finally { await page.close().catch(() => {}); }
  }
} finally {
  await browser.close();
  await new Promise(resolve => server.close(resolve));
  await new Promise(resolve => udp.close(resolve));
}
