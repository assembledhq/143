"use client";

import Link from "next/link";
import { Button } from "@/components/ui/button";

interface CtaSectionProps {
  isDark: boolean;
}

export default function CtaSection({ isDark }: CtaSectionProps) {
  return (
    <div
      className="relative flex min-h-screen items-center justify-center px-6"
      style={{
        background: isDark ? "#08080f" : "#d4e6f5",
      }}
    >
      <div className="text-center max-w-2xl space-y-6">
        <h2
          className={`text-3xl sm:text-4xl md:text-5xl font-light leading-tight tracking-tight ${
            isDark ? "text-white" : "text-slate-900"
          }`}
        >
          Built for teams that
          <br />
          ship to production
        </h2>
        <p
          className={`text-sm sm:text-base leading-relaxed max-w-md mx-auto ${
            isDark ? "text-white/40" : "text-slate-600"
          }`}
        >
          Connect your repos, bring your own coding agents, and let the
          PM optimize across bugs, projects, and tech debt — with your
          CI, your standards, your review process.
        </p>
        <div className="pt-2">
          <Button
            asChild
            className={`rounded-full px-8 py-3 text-sm font-medium transition-all ${
              isDark
                ? "bg-white text-[#08080f] hover:bg-white/90"
                : "bg-slate-900 text-white hover:bg-slate-800"
            }`}
          >
            <Link href="/login?tab=signup">
              Get Started
              <span className="ml-2">&rsaquo;</span>
            </Link>
          </Button>
        </div>
      </div>
    </div>
  );
}
