"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import Link from "next/link";
import Footer from "./footer";

interface LegalPageLayoutProps {
  title: string;
  lastUpdated: string;
  children: React.ReactNode;
}

export default function LegalPageLayout({
  title,
  lastUpdated,
  children,
}: LegalPageLayoutProps) {
  const isDark = usePrefersDark();

  return (
    <div
      className="min-h-screen flex flex-col"
      style={{ background: isDark ? "#151513" : "#f6f5f0" }}
    >
      {/* Nav */}
      <div className="flex items-center justify-between px-6 sm:px-10 pt-6 sm:pt-8">
        <Link
          href="/"
          className={`text-sm font-medium ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
        >
          &larr; 143
        </Link>
      </div>

      {/* Content */}
      <main className="flex-1 px-6 sm:px-10 py-16 sm:py-24">
        <div className="max-w-2xl mx-auto">
          <h1
            className={`mb-2 font-display text-2xl font-semibold tracking-[-0.035em] sm:text-3xl ${isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"}`}
          >
            {title}
          </h1>
          <p
            className={`mb-12 text-xs ${isDark ? "text-[#77766f]" : "text-[#85847c]"}`}
          >
            Last updated: {lastUpdated}
          </p>

          <div
            className={`space-y-8 text-sm leading-relaxed ${isDark ? "text-[#aaa89f]" : "text-[#6b6b65]"}`}
          >
            {children}
          </div>
        </div>
      </main>

      <Footer isDark={isDark} />
    </div>
  );
}

export function Section({
  heading,
  children,
}: {
  heading: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3">
      <h2 className="text-base font-medium text-inherit opacity-80">
        {heading}
      </h2>
      {children}
    </section>
  );
}
