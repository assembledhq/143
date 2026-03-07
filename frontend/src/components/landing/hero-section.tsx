"use client";

import Link from "next/link";
import { Button } from "@/components/ui/button";
import HeroCanvas, { DARK, LIGHT } from "./hero-canvas";

export { DARK, LIGHT };

interface HeroSectionProps {
  isDark: boolean;
}

export default function HeroSection({ isDark }: HeroSectionProps) {
  return (
    <div
      className="relative h-screen"
      style={{ background: isDark ? DARK.bg : "#87BBDF" }}
    >
      <HeroCanvas isDark={isDark} />

      {/* Top nav */}
      <div className="relative z-10 flex items-center justify-end px-6 sm:px-10 pt-6 sm:pt-8 pointer-events-auto">
        <Button
          asChild
          variant="outline"
          className={`rounded-full px-5 py-2 text-sm font-medium transition-all ${
            isDark
              ? "border-white/20 text-white/60 hover:text-white hover:border-white/40 bg-transparent"
              : "border-slate-400/40 text-slate-600 hover:text-slate-900 hover:border-slate-500 bg-transparent"
          }`}
        >
          <Link href="/login">Sign In</Link>
        </Button>
      </div>

      {/* Bottom-left hero */}
      <div className="relative z-10 flex min-h-[calc(100vh-80px)] flex-col justify-end px-6 sm:px-10 pb-12 sm:pb-16 select-none">
        <div className="max-w-xl space-y-5">
          <h1
            className={`text-[2.75rem] sm:text-[3.5rem] md:text-6xl font-light leading-[1.1] tracking-tight ${
              isDark ? "text-white" : "text-slate-900"
            }`}
          >
            An AI PM for
            <br />
            production engineering
            <br />
            teams
          </h1>

          <p
            className={`max-w-md text-sm sm:text-base leading-relaxed ${isDark ? "text-white/40" : "text-slate-600"}`}
          >
            Prioritizes across bugs, projects, and tech debt — then
            dispatches your coding agents to ship validated PRs.
            Works with Claude Code, Codex, Gemini CLI, or any agent
            your team already runs.
          </p>

          <div className="pt-2 pointer-events-auto">
            <Button
              asChild
              className={`rounded-full px-6 py-2.5 text-sm font-medium transition-all ${
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

      {/* Bottom-right: 143 origin story */}
      <div className={`absolute bottom-12 right-6 sm:right-10 z-10 hidden md:block max-w-[280px] text-right ${isDark ? "text-white/40" : "text-slate-600"}`}>
        <p className="text-[11px] leading-relaxed tracking-wide">
          The first US jet fighter, the P-80 Shooting Star,
          was designed and built by Lockheed&apos;s Skunk Works
          in just 143&nbsp;days. We named this project after
          that same spirit of speed.
        </p>
      </div>

      {/* Scroll indicator */}
      <div className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10">
        <div
          className={`animate-bounce ${isDark ? "text-white/30" : "text-slate-400"}`}
        >
          <svg
            width="24"
            height="24"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <polyline points="6 9 12 15 18 9" />
          </svg>
        </div>
      </div>
    </div>
  );
}
