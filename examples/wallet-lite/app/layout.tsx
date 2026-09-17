import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'NHB Wallet Lite',
  description: 'Demo wallet showcasing username registration, pay-by-email, and QR intents.',
  openGraph: {
    title: 'NHB Wallet Lite',
    description: 'Lightweight wallet that demonstrates username registration and claimables.',
    url: process.env.APP_PUBLIC_BASE || 'http://localhost:3000',
    siteName: 'NHB Wallet Lite',
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
