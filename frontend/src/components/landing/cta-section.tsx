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
        <div>
          <Button
            asChild
            className={`${type.button} rounded-full transition-all ${
              isDark
                ? "bg-white text-[#08080f] hover:bg-white/90"
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
