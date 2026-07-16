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
      style={{ background: isDark ? DARK.bg : LIGHT.bg }}
    >
      <HeroCanvas isDark={isDark} />
      <div
        className="pointer-events-none absolute inset-0 z-[1]"
        style={{
          background: isDark
            ? "radial-gradient(circle at center, rgba(17,17,15,0.92) 0%, rgba(17,17,15,0.8) 38%, rgba(17,17,15,0.28) 70%, transparent 100%)"
            : "radial-gradient(circle at center, rgba(246,245,240,0.96) 0%, rgba(246,245,240,0.84) 38%, rgba(246,245,240,0.26) 70%, transparent 100%)",
        }}
      />

      {/* Top nav */}
      <div className="pointer-events-auto relative z-10 mx-auto flex w-full max-w-[88rem] items-center justify-between px-6 pt-6 sm:px-10 sm:pt-8">
        <Link
          href="/"
          className={`${type.navBrand} ${isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"}`}
        >
          143
        </Link>

        <div className="flex items-center gap-4">
          <Button
            asChild
            variant="ghost"
            className={`${type.button} px-3 ${
              isDark
                ? "text-[#aaa89f] hover:bg-white/[0.06] hover:text-[#f4f3ee]"
                : "text-[#6b6b65] hover:bg-[#ebe9e2] hover:text-[#1b1b19]"
            }`}
          >
            <Link href="/docs">Docs</Link>
          </Button>
          <Button
            asChild
            variant="ghost"
            className={`hidden ${type.button} px-3 sm:inline-flex ${
              isDark
                ? "text-[#aaa89f] hover:bg-white/[0.06] hover:text-[#f4f3ee]"
                : "text-[#6b6b65] hover:bg-[#ebe9e2] hover:text-[#1b1b19]"
            }`}
          >
            <Link href="/login">Sign in</Link>
          </Button>
          <Button
            asChild
            className={`${type.button} rounded-full border border-transparent shadow-[0_8px_24px_rgb(49_92_232_/_18%)] transition-all ${
              isDark
                ? "bg-[#7992ff] text-[#11110f] hover:bg-[#8ba0ff]"
                : "bg-[#315ce8] text-white hover:bg-[#294fc9]"
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
        <div className="mx-auto w-full max-w-4xl space-y-7">
          <h1
            className={`max-w-4xl ${type.heroTitle} ${
              isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"
            }`}
          >
            Where your whole team builds software together
          </h1>

          <p
            className={`max-w-2xl ${type.heroBody} ${isDark ? "text-[#aaa89f]" : "text-[#6b6b65]"}`}
          >
            Run Codex, Claude Code, and OpenCode in an open-source cloud with
            shared context, previews, review loops, and automations.
          </p>

          <div className="flex flex-wrap gap-4 pt-1 pointer-events-auto">
            <Button
              asChild
              className={`${type.button} rounded-full border border-transparent shadow-[0_10px_30px_rgb(49_92_232_/_20%)] transition-all ${
                isDark
                  ? "bg-[#7992ff] text-[#11110f] hover:bg-[#8ba0ff]"
                  : "bg-[#315ce8] text-white hover:bg-[#294fc9]"
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
              className={`${type.button} rounded-full shadow-none ${
                isDark
                  ? "border-white/15 bg-[#1d1d1a]/80 text-[#dddbd4] hover:border-[#7992ff]/45 hover:bg-[#242420] hover:text-[#f4f3ee]"
                  : "border-[#cac6bb] bg-[#fefdfb]/85 text-[#252521] hover:border-[#315ce8]/40 hover:bg-[#e7ecff]/65"
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
          className={`motion-safe:animate-bounce ${isDark ? "text-[#aaa89f]/45" : "text-[#6b6b65]/55"}`}
        >
          <ChevronDown className="size-6" aria-hidden="true" />
        </div>
      </div>
    </div>
  );
}
