import type { NextConfig } from 'next';

const baseSecurityHeaders = [
  { key: 'Referrer-Policy', value: 'no-referrer' },
  { key: 'Permissions-Policy', value: 'camera=(), geolocation=(), microphone=()' },
  { key: 'Cross-Origin-Opener-Policy', value: 'same-origin' },
  { key: 'Cross-Origin-Resource-Policy', value: 'same-origin' },
  { key: 'Origin-Agent-Cluster', value: '?1' },
  { key: 'Strict-Transport-Security', value: 'max-age=31536000' },
  { key: 'X-Content-Type-Options', value: 'nosniff' },
  { key: 'X-Permitted-Cross-Domain-Policies', value: 'none' },
];

const clientDocumentHeaders = [
  {
    key: 'Content-Security-Policy',
    value: "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'none'; frame-ancestors 'none'; frame-src 'self'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'",
  },
  { key: 'X-Frame-Options', value: 'DENY' },
];

const runnerHeaders = [
  {
    key: 'Content-Security-Policy',
    value: "default-src 'none'; base-uri 'none'; child-src blob:; connect-src 'none'; font-src 'none'; form-action 'none'; frame-ancestors 'self'; frame-src data:; img-src data: blob:; media-src 'none'; object-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; worker-src blob:",
  },
  { key: 'Connection-Allowlist', value: '(response-origin);webrtc=block' },
  { key: 'X-Frame-Options', value: 'SAMEORIGIN' },
];

const nextConfig: NextConfig = {
  async headers() {
    return [
      { source: '/(.*)', headers: baseSecurityHeaders },
      { source: '/client', headers: clientDocumentHeaders },
      { source: '/client/index.html', headers: clientDocumentHeaders },
      { source: '/client/assets/runner.html', headers: runnerHeaders },
    ];
  },
  async rewrites() {
    return [{ source: '/client', destination: '/client/index.html' }];
  },
};

export default nextConfig;
