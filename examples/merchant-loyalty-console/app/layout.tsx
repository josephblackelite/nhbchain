import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Merchant Loyalty Console',
  description:
    'Configure businesses, paymasters, and loyalty programs on NHBChain. Built for operations teams managing ZNHB accruals.',
  openGraph: {
    title: 'Merchant Loyalty Console',
    description:
      'Operations console for creating businesses, assigning paymasters, and monitoring loyalty rewards on NHBChain.',
    siteName: 'Merchant Loyalty Console',
    url: process.env.APP_PUBLIC_BASE || 'http://localhost:3000',
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
