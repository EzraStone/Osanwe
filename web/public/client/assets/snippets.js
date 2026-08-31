function quoted(value) {
  return JSON.stringify(String(value));
}

export function connectionSnippets({ endpoint, paying, model = "MODEL_FROM_LIVE_CATALOG" }) {
  const url = `http://${endpoint}`;
  const tokenMode = paying === "tokens";
  const key = tokenMode ? "osanwe" : "YOUR_PROVIDER_API_KEY";
  const keyComment = tokenMode
    ? "placeholder; stripped by the local client"
    : "your provider key; handled transiently and forwarded";
  const boundary = tokenMode
    ? "The placeholder key is removed locally. A one-use token is attached for the gateway instead."
    : "Replace the placeholder with your provider key. The local client handles and forwards it for each request; it does not intentionally persist it.";
  const compatibility = "Osanwë supports the advertised live models through a strict text-only Anthropic Messages subset; some Anthropic tools and features are not supported.";

  return {
    shell: {
      code: `# tool must support ANTHROPIC_BASE_URL\nexport ANTHROPIC_BASE_URL=${url}\nexport ANTHROPIC_API_KEY=${key}   # ${keyComment}`,
      note: `${boundary} ${compatibility}`,
    },
    python: {
      code: `from anthropic import Anthropic\n\nclient = Anthropic(\n    base_url=${quoted(url)},\n    api_key=${quoted(key)},\n)`,
      note: `${boundary} Use only models shown in the live catalog. ${compatibility}`,
    },
    node: {
      code: `import Anthropic from "@anthropic-ai/sdk";\n\nconst client = new Anthropic({\n  baseURL: ${quoted(url)},\n  apiKey: ${quoted(key)},\n});`,
      note: `${boundary} Text streaming is supported for models that advertise it. ${compatibility}`,
    },
    curl: {
      code: `curl ${url}/v1/messages \\\n  -H "content-type: application/json"${tokenMode ? "" : ` \\\n  -H "x-api-key: ${key}"`} \\\n  -d '{"model":${quoted(model)},"max_tokens":1024,\n       "messages":[{"role":"user","content":"hello"}]}'`,
      note: `${boundary} The paid surface accepts POST /v1/messages and the free catalog at GET /v1/models only.`,
    },
  };
}
