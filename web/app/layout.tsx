import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Osanwë — Hosted compatibility beta',
  description: 'A private-minded AI interface for testing your own Groq or OpenAI API key.',
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className="dark">
      <body>{children}</body>
    </html>
  );
}
