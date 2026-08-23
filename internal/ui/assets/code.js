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
