// Validate before a key can be placed in any request header. Never reflect it.
export function validateProviderKey(value) {
  if (typeof value !== 'string' || !value || value.length > 4096 || /[^\x21-\x7e]/.test(value)) {
    throw new TypeError('Load one API key without spaces or line breaks (maximum 4096 characters).');
  }
  return value;
}
