"use client";

import Link from "next/link";
import { Button } from "@/components/ui/button";

interface CtaSectionProps {
  isDark: boolean;
}

export default function CtaSection({ isDark }: CtaSectionProps) {
  return (
    <div
      className="relative flex min-h-[60vh] items-center justify-center px-6"
      style={{
        background: isDark ? "#08080f" : "#d4e6f5",
      }}
    >
      <div className="text-center max-w-2xl space-y-5">
        <h2
          className={`text-2xl sm:text-3xl font-light tracking-tight ${
            isDark ? "text-white" : "text-slate-900"
          }`}
        >
          Give your team superpowers
        </h2>
        <p
          className={`text-sm leading-relaxed max-w-md mx-auto ${
            isDark ? "text-white/45" : "text-slate-600"
          }`}
        >
          Connect your repos, pick your agents, and start shipping
          as a team.
        </p>
        <div>
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
