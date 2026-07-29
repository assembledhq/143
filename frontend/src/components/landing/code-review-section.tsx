"use client";

import Link from "next/link";
import { ArrowRight, Check, CircleDot } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { useInView } from "@/hooks/use-in-view";
import {
  codeReviewApproval,
  codeReviewCapabilities,
  codeReviewEscalation,
  codeReviewOutcomes,
  codeReviewPressures,
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

function EscalationCard({ isDark }: { isDark: boolean }) {
  return (
    <div
      className={`${layout.visualFrame} border p-6 sm:p-8 ${
        isDark ? "border-white/10 bg-[#1d1d1a]" : "border-[#e1ded5] bg-[#efeee8]"
      }`}
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p
          className={`${type.cardTitle} ${isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"}`}
        >
          {codeReviewEscalation.title}
        </p>
        <span
          className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-mono font-medium uppercase tracking-wider ${
            isDark ? "bg-white/[0.08] text-[#aaa89f]" : "bg-[#dad7ce]/60 text-[#6b6b65]"
          }`}
        >
          <CircleDot className="size-3" aria-hidden="true" />
          {codeReviewEscalation.decision}
        </span>
      </div>

      <ul className="mt-5 grid gap-2">
        {codeReviewEscalation.reasons.map((reason) => (
          <li
            key={reason}
            className={`text-xs font-mono ${isDark ? "text-[#aaa89f]" : "text-[#6b6b65]"}`}
          >
            · {reason}
          </li>
        ))}
      </ul>
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

        <div className="grid gap-x-10 gap-y-8 sm:grid-cols-2 lg:grid-cols-4">
          {codeReviewPressures.map((pressure) => (
            <div
              key={pressure.title}
              className={`border-t pt-5 ${
                isDark ? "border-white/10" : "border-[#dad7ce]"
              }`}
            >
              <h3 className={`${type.cardTitle} ${heading}`}>
                {pressure.title}
              </h3>
              <p className={`mt-3 ${type.body} ${body}`}>{pressure.body}</p>
            </div>
          ))}
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
          <div className="grid gap-4">
            <ApprovalCard isDark={isDark} />
            <EscalationCard isDark={isDark} />
          </div>

          <div className={layout.copyColumn}>
            <h3 className={`${type.featureTitle} ${heading}`}>
              Auto-approval, backed by evidence.
            </h3>
            <p className={`${type.body} ${layout.copyBody} ${body}`}>
              The approval decision is made from explicit evidence, not a
              model&apos;s recommendation. Every safeguard in your policy has to
              pass first, and the reviewed commit, policy version, and agent
              output stay inspectable afterward. The number worth watching is
              the share of your pull requests that get approved this way — raise
              it by tuning the policy, not by trusting the model more.
            </p>
            <ul className="grid gap-2 pt-2">
              {codeReviewCapabilities.map((capability) => (
                <li
                  key={capability}
                  className={`text-xs font-mono ${body}`}
                >
                  · {capability}
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

        <div className="grid gap-4 md:grid-cols-2">
          {codeReviewOutcomes.map((outcome) => (
            <Card
              key={outcome.title}
              className={
                isDark
                  ? "border-white/10 bg-[#1d1d1a]"
                  : "border-[#e1ded5] bg-[#fefdfb] shadow-[0_18px_48px_-36px_rgb(36_34_28_/_32%)]"
              }
            >
              <CardContent className="p-6">
                <h3 className={`${type.cardTitle} ${heading}`}>
                  {outcome.title}
                </h3>
                <p className={`mt-4 ${type.body} ${body}`}>{outcome.body}</p>
              </CardContent>
            </Card>
          ))}
        </div>
      </div>
    </section>
  );
}
