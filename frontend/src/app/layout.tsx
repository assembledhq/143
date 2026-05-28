import { GeistSans } from "geist/font/sans";
import { GeistMono } from "geist/font/mono";
import type { Metadata, Viewport } from "next";
import { Providers } from "@/components/providers";
import { AppToaster } from "@/components/ui/app-toaster";
import "./globals.css";

export const metadata: Metadata = {
  applicationName: "143",
  title: {
    default: "143",
    template: "143 | %s",
  },
  manifest: "/manifest.webmanifest",
  icons: {
    icon: [{ url: "/icon.svg", type: "image/svg+xml" }],
    shortcut: [{ url: "/icon.svg", type: "image/svg+xml" }],
  },
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#091f33" },
    { media: "(prefers-color-scheme: dark)", color: "#091f33" },
  ],
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className={`${GeistSans.variable} ${GeistMono.variable}`} suppressHydrationWarning>
      <body className="antialiased">
        <Providers>
          {children}
          <AppToaster />
        </Providers>
      </body>
    </html>
  );
}
