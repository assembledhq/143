"use client";

import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { landingTypography as type } from "./landing-typography";

interface CtaSectionProps {
  isDark: boolean;
}

export default function CtaSection({ isDark }: CtaSectionProps) {
  return (
    <div
      className={`relative flex items-center justify-center border-t px-6 py-24 sm:px-10 sm:py-32 ${
        isDark ? "border-white/10" : "border-[#e1ded5]"
      }`}
      style={{
        background: isDark ? "#151513" : "#f6f5f0",
      }}
    >
      <div
        className={`w-full max-w-5xl space-y-6 rounded-3xl border px-6 py-16 text-center sm:px-12 sm:py-20 ${
          isDark
            ? "border-white/10 bg-[#1d1d1a] shadow-[0_26px_80px_-44px_rgb(0_0_0_/_80%)]"
            : "border-[#e1ded5] bg-[#fefdfb] shadow-[0_26px_80px_-44px_rgb(36_34_28_/_32%)]"
        }`}
      >
        <h2
          className={`${type.sectionTitle} ${
            isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"
          }`}
        >
          Put your agents to work.
        </h2>
        <div className="flex flex-wrap items-center justify-center gap-3">
          <Button
            asChild
            className={`${type.button} rounded-full border border-transparent shadow-[0_10px_30px_rgb(49_92_232_/_20%)] transition-all ${
              isDark
                ? "bg-[#7992ff] text-[#11110f] hover:bg-[#8ba0ff]"
                : "bg-[#315ce8] text-white hover:bg-[#294fc9]"
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
