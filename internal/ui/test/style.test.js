import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const css = await readFile(new URL("../assets/app.css", import.meta.url), "utf8");

function palette(selector) {
  const start = css.indexOf(`${selector}{`);
  assert.notEqual(start, -1, `missing ${selector} palette`);
  const end = css.indexOf("}", start);
  const block = css.slice(start, end);
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{6})/gi)].map((match) => [match[1], match[2]]));
}

function luminance(hex) {
  const channels = hex.slice(1).match(/../g).map((value) => parseInt(value, 16) / 255).map((value) =>
    value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4,
  );
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

function contrast(left, right) {
  const values = [luminance(left), luminance(right)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

test("faint and alarm text meet normal-text contrast in both explicit themes", () => {
  for (const selector of [':root[data-theme="light"]', ':root[data-theme="dark"]']) {
    const colors = palette(selector);
    for (const background of [colors.ground, colors.raised, colors.sunk]) {
      assert.ok(contrast(colors.faint, background) >= 4.5, `${selector} faint text contrast failed`);
      assert.ok(contrast(colors.alarm, background) >= 4.5, `${selector} alarm text contrast failed`);
    }
  }
});
