import "./globals.css";
import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "OTC Operations Console",
  description: "Operational console for OTC invoice workflows"
};

export default function RootLayout({
  children
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
