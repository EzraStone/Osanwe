export function normalizeRunnerLanguage(value) {
  if (typeof value !== "string") return "";
  const language = value.trim().toLowerCase().split(/\s+/, 1)[0];
  if (["js", "javascript", "node", "nodejs"].includes(language)) return "javascript";
  if (["html", "htm"].includes(language)) return "html";
  return "";
}

export function parseCodeFences(text) {
  if (typeof text !== "string") throw new TypeError("assistant content must be text");
  const parts = [];
  const fence = /^```([^\r\n`]*)\r?\n([\s\S]*?)^```[ \t]*$/gm;
  let cursor = 0;
  let match;
  let count = 0;
  while ((match = fence.exec(text)) && count < 64) {
    if (match.index > cursor) parts.push({ kind: "text", content: text.slice(cursor, match.index) });
    const label = match[1].trim().split(/\s+/, 1)[0].toLowerCase();
    parts.push({
      kind: "code",
      content: match[2],
      language: label,
      runnerLanguage: normalizeRunnerLanguage(label),
    });
    cursor = fence.lastIndex;
    count += 1;
  }
  if (cursor < text.length) parts.push({ kind: "text", content: text.slice(cursor) });
  if (!parts.length) parts.push({ kind: "text", content: text });
  return parts;
}

function insertBeforeLastClosingTag(source, tagName, addition, fallback) {
  const closing = new RegExp(`</${tagName}\\s*>`, "gi");
  let match;
  let last = null;
  while ((match = closing.exec(source))) last = { index: match.index, text: match[0] };
  if (!last) return fallback === "prepend" ? addition + source : source + addition;
  return source.slice(0, last.index) + addition + source.slice(last.index);
}

export function buildPreviewBundle(parts) {
  if (!Array.isArray(parts)) throw new TypeError("preview parts must be an array");
  const html = parts.find((part) => part && part.kind === "code" && part.runnerLanguage === "html");
  if (!html) return null;

  const styles = parts
    .filter((part) => part && part.kind === "code" && ["css", "stylesheet"].includes(part.language))
    .map((part) => part.content)
    .join("\n\n");
  const scripts = parts
    .filter((part) => part && part.kind === "code" && part.runnerLanguage === "javascript")
    .map((part) => part.content)
    .join("\n\n");

  let code = html.content;
  if (styles) {
    const styleTag = `<style>\n${styles.replace(/<\/style/gi, "<\\/style")}\n</style>\n`;
    code = insertBeforeLastClosingTag(code, "head", styleTag, "prepend");
  }
  if (scripts) {
    const scriptTag = `<script>\n${scripts.replace(/<\/script/gi, "<\\/script")}\n<\/script>\n`;
    code = insertBeforeLastClosingTag(code, "body", scriptTag, "append");
  }
  return { language: "html", code };
}

export function selectRunnableCode(parts) {
  if (!Array.isArray(parts)) throw new TypeError("preview parts must be an array");
  const preview = buildPreviewBundle(parts);
  if (preview) return preview;

  const runnable = parts.find(
    (part) => part && part.kind === "code" && ["html", "javascript"].includes(part.runnerLanguage),
  );
  return runnable ? { language: runnable.runnerLanguage, code: runnable.content } : null;
}
