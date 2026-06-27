"use client";

import Link from "next/link";
import { ArrowRight, ChevronDown, Github } from "lucide-react";
import { Button } from "@/components/ui/button";
import HeroCanvas, { DARK, LIGHT } from "./hero-canvas";
import { landingTypography as type } from "./landing-typography";

export { DARK, LIGHT };

interface HeroSectionProps {
  isDark: boolean;
}

export default function HeroSection({ isDark }: HeroSectionProps) {
  return (
    <div
      className="relative min-h-screen"
      style={{ background: isDark ? DARK.bg : "#FAFAFB" }}
    >
      <HeroCanvas isDark={isDark} />
      <div
        className="pointer-events-none absolute inset-0 z-[1]"
        style={{
          background: isDark
            ? "radial-gradient(circle at center, rgba(8,8,15,0.9) 0%, rgba(8,8,15,0.78) 36%, rgba(8,8,15,0.24) 68%, transparent 100%)"
            : "radial-gradient(circle at center, rgba(250,250,251,0.94) 0%, rgba(250,250,251,0.84) 36%, rgba(250,250,251,0.24) 68%, transparent 100%)",
        }}
      />

      {/* Top nav */}
      <div className="relative z-10 flex items-center justify-between px-6 pt-6 pointer-events-auto sm:px-10 sm:pt-8">
        <Link
          href="/"
          className={`${type.navBrand} ${isDark ? "text-white/85" : "text-slate-900"}`}
        >
          143
        </Link>

        <div className="flex items-center gap-4">
          <Button
            asChild
            variant="ghost"
            className={`${type.button} px-3 ${
              isDark
                ? "text-white/60 hover:bg-white/5 hover:text-white"
                : "text-slate-600 hover:bg-slate-900/5 hover:text-slate-900"
            }`}
          >
            <Link href="/docs">Docs</Link>
          </Button>
          <Button
            asChild
            variant="ghost"
            className={`hidden ${type.button} px-3 sm:inline-flex ${
              isDark
                ? "text-white/60 hover:bg-white/5 hover:text-white"
                : "text-slate-600 hover:bg-slate-900/5 hover:text-slate-900"
            }`}
          >
            <Link href="/login">Sign in</Link>
          </Button>
          <Button
            asChild
            className={`${type.button} rounded-full transition-all ${
              isDark
                ? "bg-white text-[#08080f] hover:bg-white/90"
                : "bg-slate-900 text-white hover:bg-slate-800"
            }`}
          >
            <Link href="/login?tab=signup">
              Start
              <ArrowRight className="ml-2 size-3.5" aria-hidden="true" />
            </Link>
          </Button>
        </div>
      </div>

      {/* Center hero */}
      <div className="relative z-10 flex min-h-[calc(100vh-80px)] flex-col items-center justify-center px-6 py-16 text-left select-none sm:px-10">
        <div className="mx-auto max-w-3xl space-y-6">
          <h1
            className={`max-w-3xl ${type.heroTitle} ${
              isDark ? "text-white" : "text-slate-900"
            }`}
          >
            Where your whole team builds software together
          </h1>

          <p
            className={`max-w-2xl ${type.heroBody} ${isDark ? "text-white/52" : "text-slate-600"}`}
          >
            Run Codex, Claude Code, and OpenCode in an open-source cloud with
            shared context, previews, review loops, and automations.
          </p>

          <div className="flex flex-wrap gap-4 pt-1 pointer-events-auto">
            <Button
              asChild
              className={`${type.button} rounded-full transition-all ${
                isDark
                  ? "bg-white text-[#08080f] hover:bg-white/90"
                  : "bg-slate-900 text-white hover:bg-slate-800"
              }`}
            >
              <Link href="/login?tab=signup">
                Get started
                <ArrowRight className="ml-2 size-3.5" aria-hidden="true" />
              </Link>
            </Button>
            <Button
              asChild
              variant="outline"
              className={`${type.button} rounded-md shadow-sm ${
                isDark
                  ? "border-white/15 bg-white/[0.04] text-white/80 hover:border-white/35 hover:bg-white/[0.08] hover:text-white"
                  : "border-slate-300 bg-white text-slate-900 hover:border-slate-500 hover:bg-slate-50"
              }`}
            >
              <a
                href="https://github.com/assembledhq/143"
                target="_blank"
                rel="noopener noreferrer"
              >
                <Github className="mr-2 size-3.5" aria-hidden="true" />
                View source code
              </a>
            </Button>
          </div>
        </div>
      </div>

      {/* Scroll indicator */}
      <div className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10">
        <div
          className={`animate-bounce ${isDark ? "text-white/30" : "text-slate-400"}`}
        >
          <ChevronDown className="size-6" aria-hidden="true" />
        </div>
      </div>
    </div>
  );
}
