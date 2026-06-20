"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import { useInView } from "@/hooks/use-in-view";
import { useScrollProgress } from "@/hooks/use-scroll-progress";
import Link from "next/link";
import Footer from "@/components/landing/footer";

/* ─── Staggered fade-in block ─── */
function Reveal({
  children,
  delay = 0,
  inView,
}: {
  children: React.ReactNode;
  delay?: number;
  inView: boolean;
}) {
  return (
    <div
      style={{
        opacity: inView ? 1 : 0,
        transform: inView ? "translateY(0)" : "translateY(24px)",
        transition: `opacity 0.7s ease-out ${delay}s, transform 0.7s ease-out ${delay}s`,
      }}
    >
      {children}
    </div>
  );
}

/* ─── Timeline milestone ─── */
function Milestone({
  year,
  text,
  isDark,
  inView,
  delay,
  active,
}: {
  year: string;
  text: string;
  isDark: boolean;
  inView: boolean;
  delay: number;
  active?: boolean;
}) {
  return (
    <div
      className="flex items-start gap-4"
      style={{
        opacity: inView ? 1 : 0,
        transform: inView ? "translateX(0)" : "translateX(-16px)",
        transition: `opacity 0.6s ease-out ${delay}s, transform 0.6s ease-out ${delay}s`,
      }}
    >
      {/* Dot */}
      <div className="flex flex-col items-center flex-shrink-0 pt-1">
        <div
          className="w-2.5 h-2.5 rounded-full"
          style={{
            background: active
              ? isDark
                ? "#60a5fa"
                : "#3b82f6"
              : isDark
                ? "rgba(255,255,255,0.15)"
                : "rgba(0,0,0,0.12)",
            boxShadow: active
              ? isDark
                ? "0 0 12px rgba(96,165,250,0.4)"
                : "0 0 12px rgba(59,130,246,0.3)"
              : "none",
            transition: "background 0.4s ease-out, box-shadow 0.4s ease-out",
          }}
        />
      </div>
      <div className="space-y-0.5 pb-6">
        <p
          className="text-xs font-mono tracking-wider uppercase"
          style={{
            color: active
              ? isDark
                ? "#60a5fa"
                : "#3b82f6"
              : isDark
                ? "rgba(255,255,255,0.3)"
                : "rgba(0,0,0,0.35)",
            transition: "color 0.4s ease-out",
          }}
        >
          {year}
        </p>
        <p
          className="text-sm leading-relaxed"
          style={{
            color: isDark ? "rgba(255,255,255,0.55)" : "rgba(0,0,0,0.55)",
          }}
        >
          {text}
        </p>
      </div>
    </div>
  );
}

export default function Why143Page() {
  const isDark = usePrefersDark();
  const { ref: sectionRef, inView } = useInView({ threshold: 0.85 });
  const { ref: timelineRef, progress } = useScrollProgress({
    startViewport: 0.85,
    endViewport: 0.25,
  });

  // Active milestone advances as you scroll: 0 → 1 → 2
  const activeIndex = progress < 0.33 ? 0 : progress < 0.66 ? 1 : 2;

  return (
    <div
      className="min-h-screen flex flex-col"
      style={{ background: isDark ? "#08080f" : "#d4e6f5" }}
    >
      {/* Nav */}
      <div className="flex items-center justify-between px-6 sm:px-10 pt-6 sm:pt-8">
        <Link
          href="/"
          className={`text-sm font-medium ${isDark ? "text-white/60 hover:text-white" : "text-slate-600 hover:text-slate-900"} transition-colors`}
        >
          &larr; 143
        </Link>
      </div>

      {/* Content */}
      <main
        ref={sectionRef}
        className="flex-1 px-6 sm:px-10 py-16 sm:py-24"
      >
        <div className="max-w-2xl mx-auto">
          {/* Header */}
          <Reveal inView={inView} delay={0}>
            <p
              className="text-xs font-mono tracking-wider uppercase mb-3"
              style={{
                color: isDark ? "rgba(255,255,255,0.25)" : "rgba(0,0,0,0.35)",
              }}
            >
              Why 143
            </p>
            <h1
              className={`text-2xl sm:text-3xl font-light tracking-tight ${isDark ? "text-white" : "text-slate-900"}`}
            >
              Why we&rsquo;re called 143
            </h1>
          </Reveal>

          <Reveal inView={inView} delay={0.1}>
            <p
              className="mt-6 text-sm sm:text-base leading-relaxed"
              style={{
                color: isDark ? "rgba(255,255,255,0.5)" : "rgba(0,0,0,0.5)",
              }}
            >
              In 1943, a small team of Lockheed engineers built America&rsquo;s
              first jet fighter, the XP-80 Shooting Star, in just 143 days. We
              took our name from those 143 days.
            </p>
          </Reveal>

          {/* Period photograph */}
          <Reveal inView={inView} delay={0.2}>
            <div
              className="relative rounded mt-10"
              style={{
                background: isDark ? "#1a1a1a" : "#f0ece4",
                padding: "10px 10px 32px 10px",
                boxShadow: isDark
                  ? "0 8px 32px rgba(0,0,0,0.5), 0 0 0 1px rgba(255,255,255,0.06)"
                  : "0 8px 32px rgba(0,0,0,0.1), 0 0 0 1px rgba(0,0,0,0.06)",
              }}
            >
              <div
                className="relative overflow-hidden"
                style={{ aspectRatio: "5 / 4" }}
              >
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img
                  src="/xp80-lulu-belle.jpg"
                  alt="XP-80 Shooting Star prototype 'Lulu Belle' on the tarmac at Muroc Army Airfield, 1944"
                  className="w-full h-full object-cover"
                  style={{
                    filter: `sepia(0.35) contrast(1.05) brightness(${isDark ? 0.85 : 0.95})`,
                  }}
                  loading="lazy"
                />
                {/* Photo aging overlay */}
                <div
                  className="absolute inset-0 pointer-events-none"
                  style={{
                    background:
                      "radial-gradient(ellipse at center, transparent 50%, rgba(40,30,10,0.2) 100%)",
                  }}
                />
                {/* DECLASSIFIED stamp */}
                <div
                  className="absolute pointer-events-none"
                  style={{
                    top: "6%",
                    right: "5%",
                    transform: "rotate(-8deg)",
                    border: "2px solid rgba(155,22,12,0.55)",
                    padding: "4px 12px",
                    fontFamily: '"Courier New", Courier, monospace',
                    fontSize: "clamp(11px, 2.5vw, 16px)",
                    fontWeight: "bold",
                    letterSpacing: "0.08em",
                    color: "rgba(155,22,12,0.6)",
                    textTransform: "uppercase",
                    mixBlendMode: "multiply",
                  }}
                >
                  <span
                    style={{
                      display: "inline-block",
                      borderTop: "1px solid rgba(155,22,12,0.35)",
                      borderBottom: "1px solid rgba(155,22,12,0.35)",
                      padding: "2px 0",
                    }}
                  >
                    Declassified
                  </span>
                </div>
              </div>
              {/* Caption */}
              <p
                className="text-center mt-3"
                style={{
                  fontSize: "11px",
                  letterSpacing: "0.03em",
                  color: isDark ? "rgba(255,255,255,0.5)" : "rgba(0,0,0,0.5)",
                  fontFamily: '"Courier New", Courier, monospace',
                }}
              >
                XP-80 &ldquo;Lulu Belle&rdquo; at Muroc Army Airfield, 1944
              </p>
            </div>
          </Reveal>

          {/* Narrative */}
          <Reveal inView={inView} delay={0.3}>
            <h2
              className={`mt-14 text-lg sm:text-2xl font-light leading-snug tracking-tight ${isDark ? "text-white" : "text-slate-900"}`}
            >
              Fast doesn&rsquo;t mean sloppy. It means{" "}
              <span
                style={{
                  color: isDark
                    ? "rgba(234,179,8,0.85)"
                    : "rgba(180,120,0,0.9)",
                }}
              >
                focused.
              </span>
            </h2>
            <p
              className="mt-4 text-sm sm:text-base leading-relaxed"
              style={{
                color: isDark ? "rgba(255,255,255,0.5)" : "rgba(0,0,0,0.5)",
              }}
            >
              In June 1943, Kelly Johnson set up what would become Lockheed&rsquo;s
              Skunk Works: a small team given the freedom to move fast. They
              shipped America&rsquo;s first production jet fighter in 143 days,
              ahead of schedule.
            </p>
          </Reveal>

          {/* Timeline */}
          <div ref={timelineRef} className="mt-10">
            <Milestone
              year="June 1943"
              text="The Army Air Forces gives Lockheed 180 days to build a jet fighter. No prototype to reference and no domestic jet engine expertise."
              isDark={isDark}
              inView={inView}
              delay={0.4}
              active={activeIndex >= 0}
            />
            <Milestone
              year="October 1943"
              text="The formal contract arrives, four months after work began. The mission was too important to wait for paperwork."
              isDark={isDark}
              inView={inView}
              delay={0.5}
              active={activeIndex >= 1}
            />
            <Milestone
              year="November 1943"
              text="The XP-80 Shooting Star delivers 37 days early. It flew operationally for three decades."
              isDark={isDark}
              inView={inView}
              delay={0.6}
              active={activeIndex >= 2}
            />
          </div>

          {/* Connector to today */}
          <Reveal inView={inView} delay={0.7}>
            <div
              className={`mt-6 pt-8 border-t ${isDark ? "border-white/10" : "border-slate-900/10"}`}
            >
              <p
                className="text-sm sm:text-base leading-relaxed"
                style={{
                  color: isDark ? "rgba(255,255,255,0.5)" : "rgba(0,0,0,0.5)",
                }}
              >
                That&rsquo;s the spirit we wanted to capture: a small, focused
                team with the right tools doing the impossible. It&rsquo;s why
                we&rsquo;re called 143, and it&rsquo;s how we think about the
                product we&rsquo;re building.
              </p>
              <p
                className="mt-6 text-sm"
                style={{
                  color: isDark ? "rgba(255,255,255,0.35)" : "rgba(0,0,0,0.4)",
                }}
              >
                Want the longer story of how 143 came to be?{" "}
                <Link
                  href="/about"
                  className={`underline underline-offset-2 ${isDark ? "hover:text-white/70" : "hover:text-slate-800"} transition-colors`}
                >
                  Read about the project
                </Link>
                .
              </p>
            </div>
          </Reveal>
        </div>
      </main>

      <Footer isDark={isDark} />
    </div>
  );
}
