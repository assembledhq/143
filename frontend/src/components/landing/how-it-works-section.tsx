"use client";

import Image from "next/image";
import { useInView } from "@/hooks/use-in-view";
import { platformLayers } from "./landing-copy";
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
        transform: inView ? "translateY(0)" : "translateY(32px)",
        transition: "opacity 0.7s ease-out, transform 0.7s ease-out",
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
              isDark ? "text-white/35" : "text-slate-500"
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
          isDark ? "border-white/10 bg-zinc-950" : "border-slate-200 bg-white"
        }`}
        style={{
          boxShadow: isDark
            ? "0 25px 50px -12px rgba(0,0,0,0.5), 0 0 0 1px rgba(255,255,255,0.05)"
            : "0 25px 50px -12px rgba(15,23,42,0.15), 0 0 0 1px rgba(15,23,42,0.08)",
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

/* ─── Main Section ─── */
export default function HowItWorksSection({ isDark }: HowItWorksSectionProps) {
  const label = isDark ? "text-white/25" : "text-slate-400";
  const heading = isDark ? "text-white" : "text-slate-900";
  const body = isDark ? "text-white/45" : "text-slate-600";
  const [contextLayer, executionLayer, controlLayer, previewLayer] =
    platformLayers;

  return (
    <section
      id="how-it-works"
      className={layout.sectionPadding}
      style={{ background: isDark ? "#0a0a12" : "#f2f5f9" }}
    >
      <div className="absolute inset-0 pointer-events-none">
        <div
          className={`absolute inset-x-0 top-0 h-px ${
            isDark ? "bg-white/10" : "bg-slate-300/80"
          }`}
        />
        <div
          className={`absolute left-1/2 top-0 h-full w-px ${
            isDark ? "bg-white/[0.04]" : "bg-slate-300/50"
          }`}
        />
      </div>

      <div className={`${layout.pageShell} space-y-32 sm:space-y-44`}>
        <div className={layout.sectionHeaderGrid}>
          <p className={`${type.eyebrow} ${label}`}>01 Why this matters</p>
          <div className="space-y-5">
            <h2 className={`max-w-3xl ${type.sectionTitle} ${heading}`}>
              Individual coding agents create scattered work. Teams need one
              place to run, review, and schedule it.
            </h2>
            <p className={`max-w-2xl ${type.body} ${body}`}>
              143 turns private prompts, local runs, and one-off fixes into a
              shared system with context, previews, review loops, and history
              the whole team can trust.
            </p>
          </div>
        </div>

        {/* ── Step 01: Built for Teams ── text LEFT, product screenshot RIGHT */}
        <FadeInStep>
          <div className={layout.featureRow}>
            <div className={layout.copyColumn}>
              <p className={`${type.eyebrow} ${label}`}>
                {contextLayer.step} {contextLayer.kicker}
              </p>
              <h2 className={`${type.featureTitle} ${heading}`}>
                {contextLayer.heading}
              </h2>
              <p className={`${type.body} ${layout.copyBody} ${body}`}>
                {contextLayer.body}
              </p>
              <AnimatedBulletList
                items={contextLayer.components}
                isDark={isDark}
              />
            </div>
            <ProductScreenshotFrame
              screenshot={landingScreenshots.context}
              isDark={isDark}
            />
          </div>
        </FadeInStep>

        {/* ── Step 02: Cloud Agents ── product screenshot LEFT, text RIGHT */}
        <FadeInStep>
          <div className={layout.featureRowReverse}>
            <ProductScreenshotFrame
              screenshot={landingScreenshots.execution}
              isDark={isDark}
            />
            <div className={layout.copyColumn}>
              <p className={`${type.eyebrow} ${label}`}>
                {executionLayer.step} {executionLayer.kicker}
              </p>
              <h2 className={`${type.featureTitle} ${heading}`}>
                {executionLayer.heading}
              </h2>
              <p className={`${type.body} ${layout.copyBody} ${body}`}>
                {executionLayer.body}
              </p>
              <AnimatedBulletList
                items={executionLayer.components}
                isDark={isDark}
              />
            </div>
          </div>
        </FadeInStep>

        {/* ── Step 03: Loops ── text LEFT, product screenshot RIGHT */}
        <FadeInStep>
          <div className={layout.featureRow}>
            <div className={layout.copyColumn}>
              <p className={`${type.eyebrow} ${label}`}>
                {controlLayer.step} {controlLayer.kicker}
              </p>
              <h2 className={`${type.featureTitle} ${heading}`}>
                {controlLayer.heading}
              </h2>
              <p className={`${type.body} ${layout.copyBody} ${body}`}>
                {controlLayer.body}
              </p>
              <AnimatedBulletList
                items={controlLayer.components}
                isDark={isDark}
              />
            </div>
            <ProductScreenshotFrame
              screenshot={landingScreenshots.control}
              isDark={isDark}
            />
          </div>
        </FadeInStep>

        {/* ── Step 04: Cloud Previews ── product screenshot LEFT, text RIGHT */}
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

        {/* ── Step 06: Workspace ── text LEFT, product screenshot RIGHT */}
        <FadeInStep>
          <div className={layout.featureRow}>
            <div className={layout.copyColumn}>
              <p className={`${type.eyebrow} ${label}`}>
                06 Workspace
              </p>
              <h2 className={`${type.featureTitle} ${heading}`}>
                See every run in one workspace.
              </h2>
              <p className={`${type.body} ${layout.copyBody} ${body}`}>
                Sessions, autopilot jobs, previews, PR state, usage, and audit
                logs stay visible to the team. Engineers keep full control;
                builders get scoped workflows with review safeguards.
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
