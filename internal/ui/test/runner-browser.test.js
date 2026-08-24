import assert from "node:assert/strict";
import { createSocket } from "node:dgram";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import test from "node:test";

const baseURL = process.env.OSANWE_BROWSER_TEST_URL;
const browserPath = process.env.OSANWE_CHROMIUM_PATH;
const browserFeatures = process.env.OSANWE_CHROMIUM_FEATURES || "";
const require = createRequire(import.meta.url);

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve(server.address().port));
  });
}

function close(server) {
  return new Promise((resolve) => server.close(resolve));
}

function listenDatagram(socket) {
  return new Promise((resolve, reject) => {
    socket.once("error", reject);
    socket.bind(0, "127.0.0.1", () => resolve(socket.address().port));
  });
}

function closeDatagram(socket) {
  return new Promise((resolve) => socket.close(resolve));
}

function nextDatagram(socket, timeout = 4_000) {
  return new Promise((resolve, reject) => {
    const onMessage = (message) => {
      clearTimeout(timer);
      resolve(message);
    };
    const timer = setTimeout(() => {
      socket.off("message", onMessage);
      reject(new Error("unrestricted WebRTC sent no packet to the local STUN listener"));
    }, timeout);
    socket.once("message", onMessage);
  });
}

function loadPlaywright(t) {
  try {
    return require("playwright");
  } catch (error) {
    const missingPlaywright = error && error.code === "MODULE_NOT_FOUND"
      && /^Cannot find module ['"]playwright['"]/m.test(error.message || "");
    if (!missingPlaywright) throw error;
    t.skip("optional Playwright package is not installed");
    return null;
  }
}

function openAIStream(text) {
  return [
    `data: ${JSON.stringify({ choices: [{ delta: { content: text }, finish_reason: null }] })}`,
    "",
    `data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }] })}`,
    "",
  ].join("\n");
}

test("served runner executes generated UI inside an enforced network boundary", {
  skip: !baseURL ? "set OSANWE_BROWSER_TEST_URL to an already-running Osanwë UI" : false,
  timeout: 30_000,
}, async (t) => {
  const playwright = loadPlaywright(t);
  if (!playwright) return;

  const launchOptions = {
    headless: true,
    args: browserFeatures ? [`--enable-features=${browserFeatures}`] : [],
  };
  if (browserPath) launchOptions.executablePath = browserPath;
  const browser = await playwright.chromium.launch(launchOptions);
  t.after(() => browser.close());

  const browserMajor = Number.parseInt(browser.version().split(".")[0], 10);
  assert.ok(Number.isInteger(browserMajor) && browserMajor >= 152,
    `interactive HTML requires Chromium 152+; found ${browser.version()}`);

  const calibrationSocket = createSocket("udp4");
  let calibrationPage = null;
  try {
    const calibrationPort = await listenDatagram(calibrationSocket);
    calibrationPage = await browser.newPage();
    await Promise.all([
      nextDatagram(calibrationSocket),
      calibrationPage.evaluate(async (port) => {
        for (let attempt = 0; attempt < 2; attempt += 1) {
          const peer = new RTCPeerConnection({ iceServers: [{ urls: `stun:127.0.0.1:${port}` }] });
          peer.createDataChannel(`osanwe-calibration-${attempt}`);
          await peer.setLocalDescription(await peer.createOffer());
          await new Promise((resolve) => setTimeout(resolve, 1_000));
          peer.close();
        }
      }, calibrationPort),
    ]);
  } finally {
    await Promise.allSettled([
      calibrationPage ? calibrationPage.close() : Promise.resolve(),
      closeDatagram(calibrationSocket),
    ]);
  }

  const blockedRTC = createSocket("udp4");
  let blockedRTCHits = 0;
  blockedRTC.on("message", () => { blockedRTCHits += 1; });
  let rtcPolicy = null;
  let rtcPolicyPage = null;
  try {
    const blockedRTCPort = await listenDatagram(blockedRTC);
    rtcPolicy = createServer((_request, response) => {
      response.setHeader("Content-Type", "text/html; charset=utf-8");
      response.setHeader("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; webrtc 'block'");
      response.setHeader("Connection-Allowlist", "(response-origin);webrtc=block");
      response.end(`<!doctype html><title>RTC policy probe</title><script>
      (async () => {
        let peer = null;
        try {
          peer = new RTCPeerConnection({ iceServers: [{ urls: "stun:127.0.0.1:${blockedRTCPort}" }] });
          peer.createDataChannel("osanwe-policy-probe");
          const gather = (async () => {
            await peer.setLocalDescription(await peer.createOffer());
            await new Promise((resolve) => setTimeout(resolve, 1000));
          })();
          await Promise.race([gather, new Promise((resolve) => setTimeout(resolve, 1500))]);
        } catch (_) {
          // A policy rejection is an expected successful boundary outcome.
        } finally {
          if (peer) peer.close();
          document.body.id = "rtc-probe-complete";
        }
      })();
    <\/script>`);
    });
    const rtcPolicyPort = await listen(rtcPolicy);
    rtcPolicyPage = await browser.newPage();
    await rtcPolicyPage.goto(`http://127.0.0.1:${rtcPolicyPort}/`);
    await rtcPolicyPage.locator("body#rtc-probe-complete").waitFor({ state: "attached", timeout: 5_000 });
    assert.equal(blockedRTCHits, 0, "Connection-Allowlist allowed a STUN packet to leave the policy page");
  } finally {
    await Promise.allSettled([
      rtcPolicyPage ? rtcPolicyPage.close() : Promise.resolve(),
      rtcPolicy ? close(rtcPolicy) : Promise.resolve(),
      closeDatagram(blockedRTC),
    ]);
  }

  let outsideHits = 0;
  const outside = createServer((_request, response) => {
    outsideHits += 1;
    response.setHeader("Access-Control-Allow-Origin", "*");
    response.end("reached");
  });
  const outsidePort = await listen(outside);
  t.after(() => close(outside));

  const calibrationOrigin = createServer((_request, response) => {
    response.setHeader("Content-Type", "text/html; charset=utf-8");
    response.end("<!doctype html><title>Network calibration</title>");
  });
  let networkCalibrationPage = null;
  try {
    const calibrationOriginPort = await listen(calibrationOrigin);
    networkCalibrationPage = await browser.newPage();
    await networkCalibrationPage.goto(`http://127.0.0.1:${calibrationOriginPort}/`);
    const responseText = await networkCalibrationPage.evaluate((url) => fetch(url).then((response) => response.text()),
      `http://127.0.0.1:${outsidePort}/calibration`);
    assert.equal(responseText, "reached");
    assert.ok(outsideHits > 0, "browser network calibration did not reach the loopback endpoint");
  } finally {
    await Promise.allSettled([
      networkCalibrationPage ? networkCalibrationPage.close() : Promise.resolve(),
      close(calibrationOrigin),
    ]);
  }
  outsideHits = 0;

  const freshRealmSource = [
    "<!doctype html><script>",
    "(() => {",
    "  const hostWindow = window.parent;",
    "  const nativeFetch = typeof fetch === 'function';",
    "  const finish = (outcome) => hostWindow.postMessage({ type: 'osanwe-fresh-network-probe', outcome, nativeFetch }, '*');",
    "  if (!nativeFetch) { finish('fresh realm unavailable: missing native fetch'); return; }",
    "  const controller = new AbortController();",
    `  const attempt = fetch('http://127.0.0.1:${outsidePort}/preview-escape', { signal: controller.signal })`,
    "    .then(() => 'network escaped', () => 'network blocked');",
    "  Promise.race([attempt, new Promise((resolve) => setTimeout(() => resolve('network blocked'), 1000))])",
    "    .then(finish)",
    "    .finally(() => controller.abort());",
    "})();",
    "</script>",
  ].join("\n");

  const generatedResponse = [
    "Here is a self-contained preview.",
    "```html",
    "<!doctype html><html><head><title>Generated preview</title></head><body>",
    "<main><h1 id=title>Generated app is running</h1><p id=boundary>Checking boundary</p>",
    "<button id=action type=button>Test interaction</button>",
    "<button id=explode type=button>Trigger script error</button></main>",
    "</body></html>",
    "```",
    "```css",
    "body { margin: 0; background: rgb(238, 242, 255); }",
    "main { padding: 24px; }",
    "```",
    "```javascript",
    "const boundary = document.querySelector('#boundary');",
    "const heading = document.querySelector('#title');",
    "const action = document.querySelector('#action');",
    "const explode = document.querySelector('#explode');",
    "const parent = 'generated-shadow';",
    "console.log('html-console-forwarded');",
    "console.assert(true);",
    "let storageBlocked = false;",
    "try { localStorage.setItem('test', 'value'); } catch (_) { storageBlocked = true; }",
    "const probe = document.createElement('iframe');",
    "probe.id = 'fresh-realm-probe';",
    "probe.hidden = true;",
    "const probeDeadline = setTimeout(() => { boundary.textContent = 'fresh realm unavailable: no child result'; }, 2000);",
    "const receiveProbe = (event) => {",
    "  if (event.source !== probe.contentWindow || !event.data || event.data.type !== 'osanwe-fresh-network-probe') return;",
    "  window.removeEventListener('message', receiveProbe);",
    "  clearTimeout(probeDeadline);",
    "  probe.dataset.nativeFetch = String(event.data.nativeFetch);",
    "  boundary.textContent = storageBlocked ? event.data.outcome : 'storage escaped';",
    "};",
    "window.addEventListener('message', receiveProbe);",
    `probe.srcdoc = ${JSON.stringify(freshRealmSource)};`,
    "document.body.appendChild(probe);",
    "action.onclick = () => { heading.textContent = 'Interaction passed'; };",
    "explode.onclick = () => { throw new Error('html-visible-boom'); };",
    "```",
  ].join("\n");

  let providerRequest = null;

  const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
  page.setDefaultTimeout(5_000);
  page.setDefaultNavigationTimeout(10_000);
  await page.route("**/v1/chat/completions", async (route) => {
    const request = route.request();
    providerRequest = {
      authorization: await request.headerValue("authorization"),
      body: request.postDataJSON(),
    };
    await route.fulfill({
      status: 200,
      headers: {
        "cache-control": "no-store",
        "content-type": "text/event-stream; charset=utf-8",
      },
      body: openAIStream(generatedResponse),
    });
  });
  await page.goto(baseURL);
  await page.getByRole("tab", { name: "Code", exact: true }).first().click();
  await page.locator("#runnerNetworkState").filter({ hasText: "Network off" }).waitFor();
  assert.equal(await page.locator("#runnerNetworkState").textContent(), "Network off");

  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByLabel("I understand this test boundary.").check();
  await page.getByLabel("Provider API key").fill("browser-test-key");
  await page.getByRole("button", { name: "Use for this session" }).click();
  await page.getByLabel("Your message").fill("Build the generated preview fixture.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  const generatedRun = page.getByRole("button", { name: "Run preview", exact: true }).first();
  await generatedRun.waitFor();
  assert.equal(providerRequest.authorization, "Bearer browser-test-key");
  assert.equal(providerRequest.body.messages.at(-1).content, "Build the generated preview fixture.");
  await generatedRun.click();

  const preview = page.frameLocator("#runnerPreview").frameLocator(".app-preview");
  await preview.getByRole("heading", { name: "Generated app is running" }).waitFor();
  const boundaryState = preview.locator("#boundary").filter({
    hasText: /^(?:network blocked|network escaped|storage escaped|fresh realm unavailable:)/,
  });
  await boundaryState.waitFor();
  assert.equal(await preview.locator("#boundary").textContent(), "network blocked");
  assert.equal(await preview.locator("#fresh-realm-probe").getAttribute("data-native-fetch"), "true");
  assert.equal(await preview.locator("body").evaluate((node) => getComputedStyle(node).backgroundColor), "rgb(238, 242, 255)");
  assert.equal(outsideHits, 0, "generated code reached an endpoint outside the shipped runner chain");
  await preview.getByRole("button", { name: "Test interaction" }).click();
  assert.equal(await preview.locator("#title").textContent(), "Interaction passed");
  await page.getByRole("tab", { name: "Console", exact: true }).click();
  await page.locator("#runnerResults").getByText("html-console-forwarded", { exact: false }).waitFor();
  await preview.getByRole("button", { name: "Trigger script error" }).click();
  await page.locator("#runnerStatus").getByText("html-visible-boom", { exact: false }).waitFor();
  assert.ok((await page.locator("#runnerStatus").getAttribute("class")).includes("warn"));
  await page.locator("#runnerResults").getByText("html-visible-boom", { exact: false }).waitFor();

  await page.setViewportSize({ width: 667, height: 375 });
  await page.locator("#codeRunner[role='dialog'][aria-modal='true']").waitFor();
  await page.locator(".chrome[inert]").waitFor();
  assert.equal(await page.locator("#codeRunner").getAttribute("role"), "dialog");
  assert.equal(await page.locator("#codeRunner").getAttribute("aria-modal"), "true");
  assert.equal(await page.locator(".chrome").evaluate((node) => node.inert), true);
  await page.locator("#runnerStatus").scrollIntoViewIfNeeded();
  const statusBox = await page.locator("#runnerStatus").boundingBox();
  assert.ok(statusBox && statusBox.y < 375, "runner status must remain reachable in landscape");
  await preview.getByRole("button", { name: "Test interaction" }).press("Escape");
  await page.locator("#codeRunner").waitFor({ state: "hidden" });
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.getByRole("button", { name: "Preview", exact: true }).click();

  const separator = page.getByRole("separator", { name: "Resize live preview" });
  await separator.focus();
  await separator.press("ArrowLeft");
  assert.equal(await separator.getAttribute("aria-valuetext"), "Preview width 56 percent");
  await page.getByRole("button", { name: "Maximize live preview" }).click();
  assert.equal(await page.locator("#codeRunner").getAttribute("role"), "dialog");
  assert.equal(await page.locator(".chrome").evaluate((node) => node.inert), true);
  await page.getByRole("button", { name: "Restore live preview" }).click();
  await page.locator("#codeRunner:not([role])").waitFor();

  await page.getByRole("tab", { name: "Code", exact: true }).last().click();
  await page.getByRole("combobox", { name: "Language" }).selectOption("html");
  await page.getByRole("textbox", { name: "Code" }).fill(`<!doctype html><script>
    for (let index = 0; index < 200; index += 1) console.log("overflow-line-" + index);
    throw new Error("overflow-visible-boom");
  <\/script>`);
  await page.getByRole("button", { name: "Run in preview" }).click();
  await page.locator("#runnerStatus").getByText("Run completed with errors.", { exact: true }).waitFor();
  assert.ok((await page.locator("#runnerStatus").getAttribute("class")).includes("warn"));
  await page.getByRole("tab", { name: "Console", exact: true }).click();
  await page.locator("#runnerResults").getByText("An additional HTML error occurred after the 200-line output limit.", { exact: false }).waitFor();

  await page.getByRole("tab", { name: "Code", exact: true }).last().click();
  await page.getByRole("textbox", { name: "Code" }).fill("console.log('first-snapshot')");
  await page.getByRole("combobox", { name: "Language" }).selectOption("javascript");
  await page.getByRole("button", { name: "Run in preview" }).click();
  await page.frameLocator("#runnerPreview").getByText("first-snapshot", { exact: true }).waitFor();
  const firstRunSource = await page.locator("#runnerPreview").getAttribute("src");
  await page.getByRole("tab", { name: "Code", exact: true }).last().click();
  await page.getByRole("textbox", { name: "Code" }).fill("console.log('unrun-edit')");
  await page.getByRole("button", { name: "Reload preview" }).click();
  await page.waitForFunction((previousSource) => {
    const frame = document.querySelector("#runnerPreview");
    return frame && frame.getAttribute("src") !== previousSource;
  }, firstRunSource);
  const reloadedSource = await page.locator("#runnerPreview").getAttribute("src");
  assert.notEqual(reloadedSource, firstRunSource);
  assert.match(reloadedSource, /[?&]run=/);
  await page.frameLocator("#runnerPreview").getByText("first-snapshot", { exact: true }).waitFor();
  await page.locator("#runnerStatus").getByText("Run completed. No tests were declared.", { exact: true }).waitFor();
  assert.equal(await page.frameLocator("#runnerPreview").getByText("unrun-edit", { exact: true }).count(), 0);

  await page.getByRole("tab", { name: "Code", exact: true }).last().click();
  await page.getByRole("textbox", { name: "Code" }).fill("throw new Error('visible-boom')");
  await page.getByRole("button", { name: "Run in preview" }).click();
  await page.locator("#runnerStatus").getByText("Run completed with errors.", { exact: true }).waitFor();
  assert.ok((await page.locator("#runnerStatus").getAttribute("class")).includes("warn"));
  await page.locator("#runnerResults").getByText("visible-boom", { exact: false }).waitFor();

  await page.getByRole("tab", { name: "Code", exact: true }).last().click();
  await page.getByRole("textbox", { name: "Code" }).fill("while (true) {}");
  await page.getByRole("button", { name: "Run in preview" }).click();
  await page.locator("#runnerStatus").getByText("Stopped at the 2.5 second limit.", { exact: true }).waitFor();

  await page.getByRole("tab", { name: "Code", exact: true }).last().focus();
  await page.getByRole("tab", { name: "Code", exact: true }).last().press("ArrowRight");
  assert.equal(await page.getByRole("tab", { name: "Console" }).getAttribute("aria-selected"), "true");
});
