"use client";

import { useInView } from "@/hooks/use-in-view";
import { useScrollProgress } from "@/hooks/use-scroll-progress";

interface StorySectionProps {
  isDark: boolean;
}

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
        transform: inView ? "translateY(0)" : "translateY(28px)",
        transition: `opacity 0.8s ease-out ${delay}s, transform 0.8s ease-out ${delay}s`,
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
        transition: `opacity 0.7s ease-out ${delay}s, transform 0.7s ease-out ${delay}s`,
      }}
    >
      {/* Dot + line */}
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
            transition:
              "background 0.4s ease-out, box-shadow 0.4s ease-out",
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

/* ─── Main Section ─── */
export default function StorySection({ isDark }: StorySectionProps) {
  const { ref: sectionRef, inView } = useInView({ threshold: 0.75 });
  const { ref: timelineRef, progress } = useScrollProgress({
    startViewport: 0.85,
    endViewport: 0.25,
  });

  // Active milestone advances as you scroll: 0 → 1 → 2
  const activeIndex = progress < 0.33 ? 0 : progress < 0.66 ? 1 : 2;

  const heading = isDark ? "text-white" : "text-slate-900";

  return (
    <section
      className="relative py-28 sm:py-36 px-6 sm:px-10 overflow-hidden"
      style={{ background: isDark ? "#0a0a12" : "#f2f5f9" }}
    >
      {/* ── Ambient glow ── */}
      <div
        className="absolute top-[20%] -right-[15%] w-[600px] h-[600px] rounded-full pointer-events-none"
        style={{
          background: isDark
            ? "radial-gradient(circle, rgba(234,179,8,0.04) 0%, transparent 70%)"
            : "radial-gradient(circle, rgba(234,179,8,0.06) 0%, transparent 70%)",
        }}
      />

      <div ref={sectionRef} className="relative mx-auto max-w-5xl">
        {/* ── Two-column layout ── */}
        <div className="flex flex-col lg:flex-row items-center gap-12 lg:gap-16">
          {/* Left: narrative + timeline */}
          <div className="lg:w-[45%] space-y-6">
            <Reveal inView={inView} delay={0}>
              <div className="space-y-2">
                <p
                  className="text-xs font-mono tracking-wider uppercase"
                  style={{
                    color: isDark
                      ? "rgba(255,255,255,0.25)"
                      : "rgba(0,0,0,0.35)",
                  }}
                >
                  Why 143
                </p>
                <h2
                  className={`text-2xl sm:text-3xl font-light tracking-tight ${heading}`}
                >
                  Fast doesn&rsquo;t mean
                  <br />
                  sloppy. It means{" "}
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
              </div>
            </Reveal>

            <Reveal inView={inView} delay={0.15}>
              <p
                className="text-sm sm:text-base leading-relaxed max-w-md"
                style={{
                  color: isDark
                    ? "rgba(255,255,255,0.5)"
                    : "rgba(0,0,0,0.5)",
                }}
              >
                In June 1943, Kelly Johnson set up what would become
                Lockheed&rsquo;s Skunk Works. They shipped America&rsquo;s
                first production jet fighter in 143 days.
              </p>
            </Reveal>

            {/* ── Timeline ── */}
            <div ref={timelineRef} className="pt-2">
              <Milestone
                year="June 1943"
                text="The Army Air Forces gives Lockheed 180 days to build a jet fighter. No prototype to reference and no domestic jet engine expertise."
                isDark={isDark}
                inView={inView}
                delay={0.3}
                active={activeIndex === 0}
              />
              <Milestone
                year="October 1943"
                text="The formal contract arrives, four months after work began. The mission was too important to wait for paperwork."
                isDark={isDark}
                inView={inView}
                delay={0.45}
                active={activeIndex === 1}
              />
              <Milestone
                year="November 1943"
                text="The XP-80 Shooting Star delivers 37 days early. It flew operationally for three decades."
                isDark={isDark}
                inView={inView}
                delay={0.6}
                active={activeIndex === 2}
              />
            </div>

            {/* ── Connector ── */}
            <Reveal inView={inView} delay={0.75}>
              <p
                className="text-sm sm:text-base leading-relaxed max-w-md"
                style={{
                  color: isDark
                    ? "rgba(255,255,255,0.5)"
                    : "rgba(0,0,0,0.5)",
                }}
              >
                We took our name from those 143 days.
              </p>
            </Reveal>
          </div>

          {/* Right: period photograph with DECLASSIFIED stamp */}
          <div className="lg:w-[55%] w-full">
            <Reveal inView={inView} delay={0.25}>
              <div
                className="relative rounded"
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
                    color: isDark
                      ? "rgba(255,255,255,0.5)"
                      : "rgba(0,0,0,0.5)",
                    fontFamily: '"Courier New", Courier, monospace',
                  }}
                >
                  XP-80 &ldquo;Lulu Belle&rdquo; at Muroc Army Airfield, 1944
                </p>
              </div>
            </Reveal>
          </div>
        </div>
      </div>
    </section>
  );
}
