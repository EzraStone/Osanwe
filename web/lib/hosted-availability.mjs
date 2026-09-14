// An operator can pause new provider calls without disabling the static client.
// This is a deployment setting, not an instantaneous edge switch or spend cap.
export function hostedPauseResponse(environment = process.env) {
  const setting = environment.OSANWE_HOSTED_API_PAUSED;
  if (setting === undefined || setting === '0' || setting === 'false') return null;
  // Unknown or empty configured values fail closed rather than reopening chat.
  return new Response(JSON.stringify({
    error: {
      code: 'hosted_api_paused',
      message: 'AI connections are temporarily paused. Your key has not been sent to a provider. Please try again later.',
      retryable: false,
    },
  }), {
    status: 503,
    headers: {
      'content-type': 'application/json; charset=utf-8',
      'cache-control': 'no-store, max-age=0',
      pragma: 'no-cache',
      'x-content-type-options': 'nosniff',
      'retry-after': '300',
    },
  });
}
