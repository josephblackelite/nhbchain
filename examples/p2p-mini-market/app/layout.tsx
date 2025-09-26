import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'NHB P2P Mini-Market',
  description: 'Dual-lock escrow marketplace demo for NHB ⇄ ZNHB spot trades.',
  openGraph: {
    title: 'NHB P2P Mini-Market',
    description: 'Demonstrates dual-lock escrow trades with pay intents and atomic settlement.',
    url: process.env.APP_PUBLIC_BASE || 'http://localhost:4302',
    siteName: 'NHB P2P Mini-Market',
    type: 'website'
  }
};

export default function RootLayout({
  children
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        <main>{children}</main>
      </body>
    </html>
  );
}
