export function disclosureNarrative(status, completedHere = false) {
  const provider = status && status.selected_provider ? status.selected_provider : 'the selected provider';
  const lines = [];

  if (completedHere) {
    lines.push([
      `This page completed a request through ${provider}. `,
      'The Osanwë host handled the API key, prompt, and answer transiently so it could forward the request.',
    ]);
  } else {
    lines.push([
      `If you send a request, this page will forward it to ${provider}. `,
      'The Osanwë host and provider can process the API key, prompt, answer, timing, and network metadata.',
    ]);
  }
  lines.push([
    'The provider key is kept only in this browser tab by Osanwë and is sent with each request. ',
    'Hosting and provider infrastructure may still produce security or operational logs; consult the provider policy for retention and training terms.',
  ]);
  lines.push([
    'Optional conversation history is stored only in this browser profile. ',
    'Writing style and request timing can still identify a person, so use synthetic or deliberately non-sensitive prompts during the beta.',
  ]);
  return lines;
}
