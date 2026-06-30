"use client";

import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { landingTypography as type } from "./landing-typography";

interface CtaSectionProps {
  isDark: boolean;
}

export default function CtaSection({ isDark }: CtaSectionProps) {
  const demoURL = process.env.NEXT_PUBLIC_DEMO_URL;

  return (
    <div
      className="relative flex items-center justify-center px-6 py-24 sm:py-28"
      style={{
        background: isDark ? "#0a0a12" : "#f2f5f9",
      }}
    >
      <div className="text-center max-w-2xl space-y-5">
        <h2
          className={`${type.sectionTitle} ${
            isDark ? "text-white" : "text-slate-900"
          }`}
        >
          Put your agents to work.
        </h2>
        <div className="flex flex-wrap items-center justify-center gap-3">
          {demoURL && (
            <Button
              asChild
              className={`${type.button} rounded-full transition-all ${
                isDark
                  ? "bg-white text-[#08080f] hover:bg-white/90"
                  : "bg-slate-900 text-white hover:bg-slate-800"
              }`}
            >
              <a href={demoURL}>
                Try demo
                <ArrowRight className="ml-2 size-3.5" aria-hidden="true" />
              </a>
            </Button>
          )}
          <Button
            asChild
            variant={demoURL ? "outline" : "default"}
            className={`${type.button} ${demoURL ? "rounded-md shadow-sm" : "rounded-full"} transition-all ${
              isDark
                ? demoURL
                  ? "border-white/15 bg-white/[0.04] text-white/80 hover:border-white/35 hover:bg-white/[0.08] hover:text-white"
                  : "bg-white text-[#08080f] hover:bg-white/90"
                : demoURL
                  ? "border-slate-300 bg-white text-slate-900 hover:border-slate-500 hover:bg-slate-50"
                  : "bg-slate-900 text-white hover:bg-slate-800"
            }`}
          >
            <Link href="/login?tab=signup">
              Start building
              <ArrowRight className="ml-2 size-3.5" aria-hidden="true" />
            </Link>
          </Button>
        </div>
      </div>
    </div>
  );
}
