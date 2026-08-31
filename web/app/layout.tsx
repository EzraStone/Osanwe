import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'Osanwë — Hosted bring-your-own-key beta',
  description: 'The Osanwë interface with session-only API keys for a fixed registry of AI providers.',
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
