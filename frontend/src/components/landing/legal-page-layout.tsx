"use client";

import { useEffect, useState } from "react";
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
  const [isDark, setIsDark] = useState(true);

  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => setIsDark(mq.matches);
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);

  return (
    <div
      className="min-h-screen flex flex-col"
      style={{ background: isDark ? "#08080f" : "#d4e6f5" }}
    >
      {/* Nav */}
      <div className="flex items-center justify-between px-6 sm:px-10 pt-6 sm:pt-8">
        <Link
          href="/"
          className={`text-sm font-medium ${isDark ? "text-white/60 hover:text-white" : "text-slate-600 hover:text-slate-900"} transition-colors`}
        >
          &larr; 143
        </Link>
      </div>

      {/* Content */}
      <main className="flex-1 px-6 sm:px-10 py-16 sm:py-24">
        <div className="max-w-2xl mx-auto">
          <h1
            className={`text-2xl sm:text-3xl font-light tracking-tight mb-2 ${isDark ? "text-white" : "text-slate-900"}`}
          >
            {title}
          </h1>
          <p
            className={`text-xs mb-12 ${isDark ? "text-white/30" : "text-slate-400"}`}
          >
            Last updated: {lastUpdated}
          </p>

          <div
            className={`space-y-8 text-sm leading-relaxed ${isDark ? "text-white/50" : "text-slate-600"}`}
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
