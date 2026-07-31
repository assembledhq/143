"use client";

import Link from "next/link";
import { ArrowRight, Check } from "lucide-react";
import { useInView } from "@/hooks/use-in-view";
import {
  codeReviewApproval,
  codeReviewControls,
  codeReviewSummary,
} from "./landing-copy";
import { landingLayout as layout } from "./landing-layout";
import { landingTypography as type } from "./landing-typography";

interface CodeReviewSectionProps {
  isDark: boolean;
}

function ApprovalCard({ isDark }: { isDark: boolean }) {
  return (
    <div
      className={`${layout.visualFrame} border p-6 sm:p-8 ${
        isDark ? "border-white/10 bg-[#11110f]" : "border-[#e1ded5] bg-[#fefdfb]"
      }`}
      style={{
        boxShadow: isDark
          ? "0 30px 80px -28px rgba(0,0,0,0.72), 0 0 0 1px rgba(255,255,255,0.035)"
          : "0 30px 80px -28px rgba(36,34,28,0.24), 0 8px 24px -16px rgba(36,34,28,0.16)",
      }}
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p
          className={`${type.cardTitle} ${isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"}`}
        >
          {codeReviewApproval.title}
        </p>
        <span
          className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-mono font-medium uppercase tracking-wider ${
            isDark
              ? "bg-[#7992ff]/15 text-[#9fb0ff]"
              : "bg-[#315ce8]/10 text-[#315ce8]"
          }`}
        >
          <Check className="size-3" aria-hidden="true" />
          {codeReviewApproval.decision}
        </span>
      </div>

      <dl className="mt-6 grid">
        {codeReviewApproval.evidence.map((row) => (
          <div
            key={row.label}
            className={`flex items-baseline justify-between gap-6 border-t py-3 ${
              isDark ? "border-white/[0.07]" : "border-[#e8e5dc]"
            }`}
          >
            <dt
              className={`text-xs font-mono uppercase tracking-wider ${
                isDark ? "text-[#aaa89f]/70" : "text-[#6b6b65]/80"
              }`}
            >
              {row.label}
            </dt>
            <dd
              className={`text-right text-xs font-mono ${
                isDark ? "text-[#dddbd4]" : "text-[#252521]"
              }`}
            >
              {row.value}
            </dd>
          </div>
        ))}
      </dl>

      <p
        className={`mt-6 text-xs font-mono ${isDark ? "text-[#aaa89f]/70" : "text-[#6b6b65]/80"}`}
      >
        {codeReviewApproval.footer}
      </p>
    </div>
  );
}

export default function CodeReviewSection({ isDark }: CodeReviewSectionProps) {
  const { ref, inView } = useInView({ threshold: 0.9 });
  const label = isDark ? "text-[#7992ff]" : "text-[#315ce8]";
  const heading = isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]";
  const body = isDark ? "text-[#aaa89f]" : "text-[#6b6b65]";

  return (
    <section
      id="code-review"
      className={layout.sectionPadding}
      style={{ background: isDark ? "#151513" : "#f6f5f0" }}
    >
      <div className="absolute inset-0 pointer-events-none">
        <div
          className={`absolute inset-x-0 top-0 h-px ${
            isDark ? "bg-white/10" : "bg-[#e1ded5]"
          }`}
        />
      </div>

      <div className={`${layout.pageShell} space-y-16 sm:space-y-24`}>
        <div className={layout.sectionHeaderGrid}>
          <p className={`${type.eyebrow} ${label}`}>
            {codeReviewSummary.step} {codeReviewSummary.kicker}
          </p>
          <div className="space-y-5">
            <h2 className={`max-w-3xl ${type.sectionTitle} ${heading}`}>
              {codeReviewSummary.heading}
            </h2>
            <p className={`max-w-2xl ${type.body} ${body}`}>
              {codeReviewSummary.body}
            </p>
          </div>
        </div>

        <div
          ref={ref}
          className={layout.featureRowReverse}
          style={{
            opacity: inView ? 1 : 0,
            transform: inView ? "translateY(0)" : "translateY(24px)",
            transition:
              "opacity 0.65s cubic-bezier(0.16, 1, 0.3, 1), transform 0.65s cubic-bezier(0.16, 1, 0.3, 1)",
          }}
        >
          <ApprovalCard isDark={isDark} />

          <div className={layout.copyColumn}>
            <h3 className={`${type.featureTitle} ${heading}`}>
              Tune the policy. Pick the reviewer models.
            </h3>
            <p className={`${type.body} ${layout.copyBody} ${body}`}>
              Approval follows a policy you control: size limits, sensitive
              paths, required checks. The reviewers are coding agents, not
              people. Run one model to keep costs down, or several in parallel
              for confidence.
            </p>
            <ul className="grid gap-2 pt-2">
              {codeReviewControls.map((control) => (
                <li key={control} className={`text-xs font-mono ${body}`}>
                  · {control}
                </li>
              ))}
            </ul>
            <div className="pt-2">
              <Link
                href="/docs/guides/code-review-policy"
                className={`inline-flex items-center text-sm font-medium transition-colors ${
                  isDark
                    ? "text-[#9fb0ff] hover:text-[#c2ccff]"
                    : "text-[#315ce8] hover:text-[#294fc9]"
                }`}
              >
                Configure the review policy
                <ArrowRight className="ml-2 size-3.5" aria-hidden="true" />
              </Link>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
