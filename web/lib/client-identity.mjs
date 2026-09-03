import { createHmac, randomBytes } from 'node:crypto';

const processSalt = randomBytes(32);

function sourceAddress(request) {
  const cloudflare = request.headers.get('cf-connecting-ip');
  if (cloudflare) return cloudflare.slice(0, 80);
  const forwarded = request.headers.get('x-forwarded-for')?.split(',')[0]?.trim();
  if (forwarded) return forwarded.slice(0, 80);
  return 'local';
}

export function ephemeralClientIdentity(request) {
  return createHmac('sha256', processSalt)
    .update(sourceAddress(request))
    .digest('base64url')
    .slice(0, 24);
}
