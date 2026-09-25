export function validateCatalog(value) {
  const invalid = () => { throw new Error('Provider suggestions are unavailable. Try again.'); };
  if (!value || !Array.isArray(value.providers) || !value.providers.length || value.providers.length > 32) invalid();
  const seen = new Set();
  return value.providers.map(item => {
    if (!item || typeof item.id !== 'string' || !/^[a-z0-9-]{1,40}$/.test(item.id) || seen.has(item.id) ||
        typeof item.label !== 'string' || !item.label.trim() || item.label.length > 80 ||
        !Array.isArray(item.models) || item.models.length > 100 ||
        item.models.some(model => typeof model !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,159}$/.test(model))) invalid();
    seen.add(item.id);
    return { id: item.id, label: item.label, models: [...new Set(item.models)] };
  });
}
