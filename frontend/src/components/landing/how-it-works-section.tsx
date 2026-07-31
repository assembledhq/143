"use client";

import Image from "next/image";
import { useInView } from "@/hooks/use-in-view";
import { codingAgents, platformLayers } from "./landing-copy";
import { landingLayout as layout } from "./landing-layout";
import { landingScreenshots } from "./landing-screenshots";
import { landingTypography as type } from "./landing-typography";

interface HowItWorksSectionProps {
  isDark: boolean;
}

/* ─── Fade-in wrapper ─── */
function FadeInStep({ children }: { children: React.ReactNode }) {
  const { ref, inView } = useInView({ threshold: 0.85 });

  return (
    <div
      ref={ref}
      style={{
        opacity: inView ? 1 : 0,
        transform: inView ? "translateY(0)" : "translateY(24px)",
        transition: "opacity 0.65s cubic-bezier(0.16, 1, 0.3, 1), transform 0.65s cubic-bezier(0.16, 1, 0.3, 1)",
      }}
    >
      {children}
    </div>
  );
}

function AnimatedBulletList({
  items,
  isDark,
}: {
  items: readonly string[];
  isDark: boolean;
}) {
  const { ref, inView } = useInView({ threshold: 0.55 });

  return (
    <div ref={ref}>
      <ul className="grid gap-2 pt-2">
        {items.map((item, index) => (
          <li
            key={item}
            className={`text-xs font-mono transition-all duration-500 ${
              isDark ? "text-[#aaa89f]" : "text-[#6b6b65]"
            }`}
            style={{
              opacity: inView ? 1 : 0,
              transform: inView ? "translateY(0)" : "translateY(12px)",
              transitionDelay: `${index * 90}ms`,
            }}
          >
            · {item}
          </li>
        ))}
      </ul>
    </div>
  );
}

function ProductScreenshotFrame({
  screenshot,
  isDark,
}: {
  screenshot: (typeof landingScreenshots)[keyof typeof landingScreenshots];
  isDark: boolean;
}) {
  return (
    <div className={layout.visualColumn}>
      <div
        className={`${layout.visualFrame} aspect-[16/9] border ${
          isDark ? "border-white/10 bg-[#11110f]" : "border-[#e1ded5] bg-[#fefdfb]"
        }`}
        style={{
          boxShadow: isDark
            ? "0 30px 80px -28px rgba(0,0,0,0.72), 0 0 0 1px rgba(255,255,255,0.035)"
            : "0 30px 80px -28px rgba(36,34,28,0.24), 0 8px 24px -16px rgba(36,34,28,0.16)",
        }}
      >
        <Image
          src={screenshot.src}
          alt={screenshot.alt}
          width={1440}
          height={900}
          sizes="(min-width: 1024px) 58vw, 100vw"
          className="h-full w-full object-cover object-top"
        />
      </div>
    </div>
  );
}

function CodingAgentStrip({ isDark }: { isDark: boolean }) {
  return (
    <div className="flex flex-wrap gap-3">
      {codingAgents.map((agent) => (
        <div
          key={agent.name}
          className={`flex items-center gap-3 rounded-xl border px-5 py-3.5 ${
            isDark
              ? "border-white/10 bg-[#1d1d1a]"
              : "border-[#e1ded5] bg-[#fefdfb] shadow-[0_18px_48px_-36px_rgb(36_34_28_/_32%)]"
          }`}
        >
          <Image
            src={agent.logo}
            alt={`${agent.name} logo`}
            width={20}
            height={20}
          />
          <span
            className={`${type.cardTitle} ${
              isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"
            }`}
          >
            {agent.name}
          </span>
        </div>
      ))}
    </div>
  );
}

/* ─── Main Section ─── */
export default function HowItWorksSection({ isDark }: HowItWorksSectionProps) {
  const label = isDark ? "text-[#7992ff]" : "text-[#315ce8]";
  const heading = isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]";
  const body = isDark ? "text-[#aaa89f]" : "text-[#6b6b65]";
  const [agentLayer, previewLayer] = platformLayers;

  return (
    <section
      id="how-it-works"
      className={layout.sectionPadding}
      style={{ background: isDark ? "#151513" : "#f6f5f0" }}
    >
      <div className="absolute inset-0 pointer-events-none">
        <div
          className={`absolute inset-x-0 top-0 h-px ${
            isDark ? "bg-white/10" : "bg-[#e1ded5]"
          }`}
        />
        <div
          className={`absolute left-1/2 top-0 h-full w-px ${
            isDark ? "bg-white/[0.035]" : "bg-[#e1ded5]/70"
          }`}
        />
      </div>

      <div className={`${layout.pageShell} space-y-28 sm:space-y-44`}>
        {/* ── 02 Any agent ── text LEFT, product screenshot RIGHT */}
        <FadeInStep>
          <div className="space-y-12">
            <div className={layout.featureRow}>
              <div className={layout.copyColumn}>
                <p className={`${type.eyebrow} ${label}`}>
                  {agentLayer.step} {agentLayer.kicker}
                </p>
                <h2 className={`${type.featureTitle} ${heading}`}>
                  {agentLayer.heading}
                </h2>
                <p className={`${type.body} ${layout.copyBody} ${body}`}>
                  {agentLayer.body}
                </p>
                <AnimatedBulletList
                  items={agentLayer.components}
                  isDark={isDark}
                />
              </div>
              <ProductScreenshotFrame
                screenshot={landingScreenshots.execution}
                isDark={isDark}
              />
            </div>
            <CodingAgentStrip isDark={isDark} />
          </div>
        </FadeInStep>

        {/* ── 03 Previews ── product screenshot LEFT, text RIGHT */}
        <FadeInStep>
          <div className={layout.featureRowReverse}>
            <ProductScreenshotFrame
              screenshot={landingScreenshots.preview}
              isDark={isDark}
            />
            <div className={layout.copyColumn}>
              <p className={`${type.eyebrow} ${label}`}>
                {previewLayer.step} {previewLayer.kicker}
              </p>
              <h2 className={`${type.featureTitle} ${heading}`}>
                {previewLayer.heading}
              </h2>
              <p className={`${type.body} ${layout.copyBody} ${body}`}>
                {previewLayer.body}
              </p>
              <AnimatedBulletList
                items={previewLayer.components}
                isDark={isDark}
              />
            </div>
          </div>
        </FadeInStep>

        {/* ── 04 Workspace ── text LEFT, product screenshot RIGHT */}
        <FadeInStep>
          <div className={layout.featureRow}>
            <div className={layout.copyColumn}>
              <p className={`${type.eyebrow} ${label}`}>
                04 Workspace
              </p>
              <h2 className={`${type.featureTitle} ${heading}`}>
                See every run in one workspace.
              </h2>
              <p className={`${type.body} ${layout.copyBody} ${body}`}>
                Sessions, previews, PR state, usage, and audit logs are visible
                to the whole team. Repair loops fix failing checks before
                anyone is asked to review. Engineers keep full control, and
                builders get scoped workflows.
              </p>
            </div>
            <ProductScreenshotFrame
              screenshot={landingScreenshots.workspace}
              isDark={isDark}
            />
          </div>
        </FadeInStep>
      </div>
    </section>
  );
}
