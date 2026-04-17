"use client";

import RadarCanvas from "./radar-canvas";
import { useInView } from "@/hooks/use-in-view";
import { useScrollProgress } from "@/hooks/use-scroll-progress";

interface HowItWorksSectionProps {
  isDark: boolean;
}

const dispatches = [
  {
    agent: "Claude Code",
    task: "Fix null ref in auth flow",
    source: "Sentry",
    status: "PR open",
    statusColor: "green" as const,
    accent: "bg-green-400",
    accentRgb: "34,197,94",
  },
  {
    agent: "Codex",
    task: "Add session store schema",
    source: "Project",
    status: "Building",
    statusColor: "blue" as const,
    accent: "bg-blue-400",
    accentRgb: "59,130,246",
  },
  {
    agent: "Gemini CLI",
    task: "Update deprecated API calls",
    source: "Tech Debt",
    status: "CI running",
    statusColor: "yellow" as const,
    accent: "bg-yellow-400",
    accentRgb: "250,204,21",
  },
];

const terminalLines = [
  {
    prefix: "AGENT",
    prefixColor: "text-yellow-400",
    text: "claude-code spinning up in cloud sandbox",
    threshold: 0.15,
  },
  {
    prefix: "EXEC",
    prefixColor: "text-orange-400",
    text: "running: fix null ref in auth flow",
    threshold: 0.3,
  },
  {
    prefix: "PREV",
    prefixColor: "text-blue-400",
    text: "preview env ready → preview-342.143.dev",
    threshold: 0.45,
  },
  {
    prefix: "PASS",
    prefixColor: "text-green-400",
    text: "CI 47/47 · PR #342 ready for review",
    threshold: 0.6,
  },
];

const projectTasks = [
  { task: "Baseline: p95 latency 420ms", baseStatus: "done" as const },
  { task: "Iteration 1: optimize DB queries → 380ms", baseStatus: "done" as const },
  {
    task: "Iteration 2: add response caching → 310ms",
    baseStatus: "active" as const,
    completeAt: 0.2,
  },
  {
    task: "Iteration 3: reduce serialization → 265ms",
    baseStatus: "pending" as const,
    activateAt: 0.2,
    completeAt: 0.4,
  },
  {
    task: "Target: p95 latency < 250ms",
    baseStatus: "pending" as const,
    activateAt: 0.4,
    completeAt: 0.6,
  },
];

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

/* ─── Step 02: Terminal with typewriter reveal ─── */
function TerminalContent({ progress }: { progress: number }) {
  return (
    <div className="p-5 font-mono text-sm space-y-3">
      {terminalLines.map((line) => {
        const lineProgress =
          progress >= line.threshold
            ? Math.min(1, (progress - line.threshold) / 0.12)
            : 0;

        const visible = lineProgress > 0;
        const charsToShow = visible
          ? Math.ceil(line.text.length * lineProgress)
          : 0;
        const visibleText = visible ? line.text.slice(0, charsToShow) : "";

        return (
          <div
            key={line.prefix}
            className="flex gap-3"
            style={{
              opacity: visible ? Math.min(1, lineProgress * 3) : 0,
              transition: "opacity 0.15s ease-out",
            }}
          >
            <span className={`flex-shrink-0 ${line.prefixColor}`}>
              [{line.prefix}]
            </span>
            <span className="text-white/50">
              {visibleText || "\u00A0"}
              {visible && lineProgress < 1 && (
                <span className="animate-pulse">_</span>
              )}
            </span>
          </div>
        );
      })}

      <div
        className="flex gap-3 pt-1 text-white/20"
        style={{
          opacity: progress >= 0.75 ? Math.min(1, (progress - 0.75) / 0.1) : 0,
          transition: "opacity 0.3s ease-out",
        }}
      >
        <span>{">"}</span>
        <span>
          done · team notified · PR visible to all
          <span className="animate-pulse">_</span>
        </span>
      </div>
    </div>
  );
}

/* ─── Step 03: Projects with progressive completion ─── */
function ProjectsContent({
  isDark,
  progress,
}: {
  isDark: boolean;
  progress: number;
}) {
  const resolvedTasks = projectTasks.map((t) => {
    if (t.baseStatus === "done") return { ...t, status: "done" as const };
    if ("completeAt" in t && t.completeAt !== undefined && progress >= t.completeAt)
      return { ...t, status: "done" as const };
    if ("activateAt" in t && t.activateAt !== undefined && progress >= t.activateAt)
      return { ...t, status: "active" as const };
    if (t.baseStatus === "active") return { ...t, status: "active" as const };
    return { ...t, status: "pending" as const };
  });

  const doneCount = resolvedTasks.filter((t) => t.status === "done").length;
  const barPercent = (doneCount / projectTasks.length) * 100;

  return (
    <>
      {/* Project header + progress */}
      <div
        className={`px-5 pt-4 pb-3 border-b ${
          isDark ? "border-white/[0.06]" : "border-slate-100"
        }`}
      >
        <div className="flex items-center justify-between mb-3">
          <span
            className={`text-sm font-medium ${
              isDark ? "text-white/80" : "text-slate-800"
            }`}
          >
            Loop: reduce API latency
          </span>
          <span
            className={`text-xs font-mono ${
              isDark ? "text-white/30" : "text-slate-400"
            }`}
          >
            {doneCount}/5
          </span>
        </div>
        {/* Progress bar */}
        <div
          className={`h-1.5 rounded-full overflow-hidden ${
            isDark ? "bg-white/[0.06]" : "bg-slate-100"
          }`}
        >
          <div
            className="h-full rounded-full"
            style={{
              width: `${barPercent}%`,
              background: "linear-gradient(90deg, #3b82f6, #60a5fa)",
              transition: "width 0.5s ease-out",
            }}
          />
        </div>
      </div>
      {/* Task list */}
      <div className="px-5 py-3">
        <div className="space-y-2.5">
          {resolvedTasks.map((item) => (
            <div
              key={item.task}
              className="flex items-center gap-3"
              style={{ transition: "opacity 0.3s ease-out" }}
            >
              <span className="flex-shrink-0 w-4 flex justify-center">
                {item.status === "done" ? (
                  <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                    <circle
                      cx="8"
                      cy="8"
                      r="7"
                      className={
                        isDark
                          ? "fill-green-400/20 stroke-green-400/60"
                          : "fill-green-50 stroke-green-500"
                      }
                      strokeWidth="1"
                      style={{ transition: "fill 0.4s, stroke 0.4s" }}
                    />
                    <path
                      d="M5 8l2 2 4-4"
                      className={
                        isDark ? "stroke-green-400" : "stroke-green-600"
                      }
                      strokeWidth="1.5"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      fill="none"
                    />
                  </svg>
                ) : item.status === "active" ? (
                  <span className="relative flex h-3 w-3">
                    <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-50" />
                    <span
                      className={`relative inline-flex rounded-full h-3 w-3 ${
                        isDark ? "bg-blue-400" : "bg-blue-500"
                      }`}
                    />
                  </span>
                ) : (
                  <span
                    className={`inline-flex h-3 w-3 rounded-full border ${
                      isDark ? "border-white/10" : "border-slate-300"
                    }`}
                  />
                )}
              </span>
              <span
                className={`text-sm font-mono transition-colors duration-400 ${
                  item.status === "done"
                    ? isDark
                      ? "text-white/30 line-through"
                      : "text-slate-400 line-through"
                    : item.status === "active"
                      ? isDark
                        ? "text-white/80"
                        : "text-slate-800"
                      : isDark
                        ? "text-white/30"
                        : "text-slate-400"
                }`}
              >
                {item.task}
              </span>
            </div>
          ))}
        </div>
      </div>
    </>
  );
}

/* ─── Step 04: Dispatch board with staggered reveal ─── */
function DispatchContent({
  isDark,
  progress,
}: {
  isDark: boolean;
  progress: number;
}) {
  const thresholds = [0.2, 0.4, 0.6];
  const visibleCount = thresholds.filter((t) => progress >= t).length;

  return (
    <>
      {/* Window chrome */}
      <div
        className={`flex items-center gap-1.5 px-4 py-3 border-b ${
          isDark ? "border-white/[0.06]" : "border-slate-200"
        }`}
      >
        <div className="w-2.5 h-2.5 rounded-full bg-[#ff5f57]" />
        <div className="w-2.5 h-2.5 rounded-full bg-[#febc2e]" />
        <div className="w-2.5 h-2.5 rounded-full bg-[#28c840]" />
        <span
          className={`ml-3 text-xs font-mono ${
            isDark ? "text-white/25" : "text-slate-400"
          }`}
        >
          Dispatch Board
        </span>
        <div className="ml-auto flex items-center gap-1.5">
          {visibleCount > 0 && (
            <>
              <span className="relative flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-green-400" />
              </span>
              <span
                className={`text-xs font-mono ${
                  isDark ? "text-green-400/60" : "text-green-600"
                }`}
              >
                {visibleCount} active
              </span>
            </>
          )}
        </div>
      </div>

      {/* Agent rows */}
      {dispatches.map((d, i) => {
        const rowThreshold = thresholds[i];
        const rowProgress =
          progress >= rowThreshold
            ? Math.min(1, (progress - rowThreshold) / 0.15)
            : 0;

        const visible = rowProgress > 0;

        // Brief accent highlight that fades
        const highlightOpacity = visible
          ? rowProgress < 0.5
            ? rowProgress * 0.2
            : Math.max(0, 0.1 - (rowProgress - 0.5) * 0.2)
          : 0;

        const statusColors = {
          green: isDark
            ? "text-green-400/80 bg-green-400/10"
            : "text-green-700 bg-green-50",
          blue: isDark
            ? "text-blue-400/80 bg-blue-400/10"
            : "text-blue-700 bg-blue-50",
          yellow: isDark
            ? "text-yellow-400/80 bg-yellow-400/10"
            : "text-yellow-700 bg-yellow-50",
        };
        const colors = statusColors[d.statusColor];

        return (
          <div
            key={d.agent}
            className={`flex items-center gap-4 px-5 py-3.5 border-b ${
              isDark ? "border-white/[0.04]" : "border-slate-100"
            }`}
            style={{
              opacity: visible ? rowProgress : 0,
              transition: "opacity 0.4s ease-out, background 0.4s ease-out",
              background:
                highlightOpacity > 0
                  ? `rgba(${d.accentRgb}, ${highlightOpacity})`
                  : "transparent",
            }}
          >
            {/* Colored accent line */}
            <div
              className={`w-0.5 h-8 rounded-full flex-shrink-0 ${d.accent}`}
            />
            <div className="flex-1 min-w-0">
              <span
                className={`text-sm font-mono font-medium ${
                  isDark ? "text-white/70" : "text-slate-800"
                }`}
              >
                {d.agent}
              </span>
              <p
                className={`text-xs truncate mt-0.5 ${
                  isDark ? "text-white/30" : "text-slate-500"
                }`}
              >
                {d.task}
              </p>
            </div>
            <span
              className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-mono flex-shrink-0 ${colors}`}
            >
              {d.status}
            </span>
          </div>
        );
      })}

      {/* Footer */}
      <div
        className={`px-5 py-3 ${
          isDark ? "bg-white/[0.01]" : "bg-slate-50/50"
        }`}
        style={{
          opacity: progress >= 0.8 ? Math.min(1, (progress - 0.8) / 0.1) : 0,
          transition: "opacity 0.3s ease-out",
        }}
      >
        <p
          className={`text-xs font-mono ${
            isDark ? "text-white/20" : "text-slate-400"
          }`}
        >
          2 PRs shipped today &middot; next cycle in 1h 42m
        </p>
      </div>
    </>
  );
}

/* ─── Main Section ─── */
export default function HowItWorksSection({ isDark }: HowItWorksSectionProps) {
  const label = isDark ? "text-white/25" : "text-slate-400";
  const heading = isDark ? "text-white" : "text-slate-900";
  const body = isDark ? "text-white/45" : "text-slate-600";

  const { ref: termRef, progress: termProgress } = useScrollProgress();
  const { ref: projRef, progress: projProgress } = useScrollProgress();
  const { ref: dispRef, progress: dispProgress } = useScrollProgress();

  return (
    <section
      className="relative py-32 sm:py-40 px-6 sm:px-10 overflow-hidden"
      style={{ background: isDark ? "#0a0a12" : "#f2f5f9" }}
    >
      {/* Ambient glow orbs */}
      <div
        className="absolute top-[15%] -left-[10%] w-[500px] h-[500px] rounded-full pointer-events-none"
        style={{
          background: isDark
            ? "radial-gradient(circle, rgba(34,197,94,0.06) 0%, transparent 70%)"
            : "radial-gradient(circle, rgba(34,197,94,0.08) 0%, transparent 70%)",
        }}
      />
      <div
        className="absolute top-[45%] -right-[10%] w-[600px] h-[600px] rounded-full pointer-events-none"
        style={{
          background: isDark
            ? "radial-gradient(circle, rgba(59,130,246,0.05) 0%, transparent 70%)"
            : "radial-gradient(circle, rgba(59,130,246,0.06) 0%, transparent 70%)",
        }}
      />
      <div
        className="absolute bottom-[10%] -left-[5%] w-[400px] h-[400px] rounded-full pointer-events-none"
        style={{
          background: isDark
            ? "radial-gradient(circle, rgba(168,85,247,0.04) 0%, transparent 70%)"
            : "radial-gradient(circle, rgba(168,85,247,0.05) 0%, transparent 70%)",
        }}
      />

      <div className="relative mx-auto max-w-5xl space-y-40 sm:space-y-52">
        {/* ── Step 01: Built for Teams ── text LEFT, radar RIGHT */}
        <FadeInStep>
          <div className="flex flex-col md:flex-row items-center gap-12 md:gap-20">
            <div className="md:w-1/2 space-y-4">
              <p
                className={`text-xs font-mono tracking-wider uppercase ${label}`}
              >
                Step 01
              </p>
              <h2
                className={`text-2xl sm:text-3xl font-light tracking-tight ${heading}`}
              >
                Built for teams,
                <br />
                not solo devs
              </h2>
              <p className={`text-sm leading-relaxed max-w-sm ${body}`}>
                By default, every automation, prompt, and agent run is visible
                to your whole team. Non-engineers can write prompts, kick off
                runs, and contribute code &mdash; and the team sees exactly
                what was asked for and what was produced.
              </p>
            </div>
            <div className="md:w-1/2 w-full max-w-[400px] md:max-w-none relative">
              {/* Glow behind radar */}
              <div
                className="absolute inset-0 -m-8 rounded-full pointer-events-none"
                style={{
                  background: isDark
                    ? "radial-gradient(circle, rgba(34,197,94,0.08) 0%, transparent 60%)"
                    : "radial-gradient(circle, rgba(34,197,94,0.1) 0%, transparent 60%)",
                }}
              />
              <div className="relative aspect-square rounded-lg overflow-hidden">
                <RadarCanvas isDark={isDark} />
              </div>
            </div>
          </div>
        </FadeInStep>

        {/* ── Step 02: Cloud Agents ── terminal LEFT, text RIGHT (flipped) */}
        <FadeInStep>
          <div
            ref={termRef}
            className="flex flex-col md:flex-row-reverse items-center gap-12 md:gap-20"
          >
            <div className="md:w-1/2 space-y-4">
              <p
                className={`text-xs font-mono tracking-wider uppercase ${label}`}
              >
                Step 02
              </p>
              <h2
                className={`text-2xl sm:text-3xl font-light tracking-tight ${heading}`}
              >
                Your agents,
                <br />
                running in the cloud
              </h2>
              <p className={`text-sm leading-relaxed max-w-sm ${body}`}>
                The big labs are constantly one-upping each other. With 143,
                there&rsquo;s no vendor lock-in &mdash; use Claude Code, Codex,
                or whatever comes next. When a better model drops, swap it in.
                Everything runs in sandboxed cloud containers with preview
                environments.
              </p>
            </div>
            <div className="md:w-1/2 relative">
              {/* Glow behind terminal */}
              <div
                className="absolute inset-0 -m-6 rounded-full pointer-events-none"
                style={{
                  background: isDark
                    ? "radial-gradient(circle, rgba(251,191,36,0.06) 0%, transparent 60%)"
                    : "radial-gradient(circle, rgba(251,191,36,0.08) 0%, transparent 60%)",
                }}
              />
              {/* Terminal window */}
              <div
                className="relative rounded-xl overflow-hidden shadow-2xl"
                style={{
                  background: isDark ? "#111119" : "#1e1e2e",
                  boxShadow: isDark
                    ? "0 25px 50px -12px rgba(0,0,0,0.5), 0 0 0 1px rgba(255,255,255,0.05)"
                    : "0 25px 50px -12px rgba(0,0,0,0.25), 0 0 0 1px rgba(0,0,0,0.1)",
                }}
              >
                {/* Window chrome */}
                <div className="flex items-center gap-1.5 px-4 py-3 border-b border-white/[0.06]">
                  <div className="w-2.5 h-2.5 rounded-full bg-[#ff5f57]" />
                  <div className="w-2.5 h-2.5 rounded-full bg-[#febc2e]" />
                  <div className="w-2.5 h-2.5 rounded-full bg-[#28c840]" />
                  <span className="ml-3 text-xs font-mono text-white/25">
                    143 · cloud agent
                  </span>
                </div>
                <TerminalContent progress={termProgress} />
              </div>
            </div>
          </div>
        </FadeInStep>

        {/* ── Step 03: Loops ── text LEFT, app window RIGHT */}
        <FadeInStep>
          <div
            ref={projRef}
            className="flex flex-col md:flex-row items-center gap-12 md:gap-20"
          >
            <div className="md:w-1/2 space-y-4">
              <p
                className={`text-xs font-mono tracking-wider uppercase ${label}`}
              >
                Step 03
              </p>
              <h2
                className={`text-2xl sm:text-3xl font-light tracking-tight ${heading}`}
              >
                Loops: define an eval,
                <br />
                let agents improve it
              </h2>
              <p className={`text-sm leading-relaxed max-w-sm ${body}`}>
                Want better latency? Define a benchmark. 143 runs your coding
                agent in a loop &mdash; each iteration measures against the eval
                and hill-climbs toward the target. Works for test coverage,
                bundle size, error rates, or anything measurable.
              </p>
            </div>
            <div className="md:w-1/2 relative">
              {/* Glow behind app window */}
              <div
                className="absolute inset-0 -m-6 rounded-full pointer-events-none"
                style={{
                  background: isDark
                    ? "radial-gradient(circle, rgba(59,130,246,0.06) 0%, transparent 60%)"
                    : "radial-gradient(circle, rgba(59,130,246,0.08) 0%, transparent 60%)",
                }}
              />
              {/* App window */}
              <div
                className="relative rounded-xl overflow-hidden shadow-2xl"
                style={{
                  background: isDark ? "#111119" : "#ffffff",
                  boxShadow: isDark
                    ? "0 25px 50px -12px rgba(0,0,0,0.5), 0 0 0 1px rgba(255,255,255,0.05)"
                    : "0 25px 50px -12px rgba(0,0,0,0.15), 0 0 0 1px rgba(0,0,0,0.08)",
                }}
              >
                {/* Window chrome */}
                <div
                  className={`flex items-center gap-1.5 px-4 py-3 border-b ${
                    isDark ? "border-white/[0.06]" : "border-slate-200"
                  }`}
                >
                  <div className="w-2.5 h-2.5 rounded-full bg-[#ff5f57]" />
                  <div className="w-2.5 h-2.5 rounded-full bg-[#febc2e]" />
                  <div className="w-2.5 h-2.5 rounded-full bg-[#28c840]" />
                  <span
                    className={`ml-3 text-xs font-mono ${
                      isDark ? "text-white/25" : "text-slate-400"
                    }`}
                  >
                    Loops
                  </span>
                </div>
                <ProjectsContent isDark={isDark} progress={projProgress} />
                {/* Footer */}
                <div
                  className={`px-5 py-3 border-t ${
                    isDark ? "border-white/[0.06]" : "border-slate-100"
                  }`}
                >
                  <p
                    className={`text-xs font-mono ${
                      isDark ? "text-white/20" : "text-slate-400"
                    }`}
                  >
                    eval: p95 latency &lt; 250ms &middot; 3 iterations complete
                  </p>
                </div>
              </div>
            </div>
          </div>
        </FadeInStep>

        {/* ── Step 04: Full Transparency ── dashboard LEFT, text RIGHT (flipped) */}
        <FadeInStep>
          <div
            ref={dispRef}
            className="flex flex-col md:flex-row-reverse items-center gap-12 md:gap-20"
          >
            <div className="md:w-1/2 space-y-4">
              <p
                className={`text-xs font-mono tracking-wider uppercase ${label}`}
              >
                Step 04
              </p>
              <h2
                className={`text-2xl sm:text-3xl font-light tracking-tight ${heading}`}
              >
                Full visibility
                <br />
                across the team
              </h2>
              <p className={`text-sm leading-relaxed max-w-sm ${body}`}>
                By default, every agent run, prompt, and automation is shared
                with the team &mdash; all in one place. No more wondering
                what other engineers are working on or duplicating effort.
              </p>
            </div>
            <div className="md:w-1/2 relative">
              {/* Glow behind dashboard */}
              <div
                className="absolute inset-0 -m-6 rounded-full pointer-events-none"
                style={{
                  background: isDark
                    ? "radial-gradient(circle, rgba(168,85,247,0.05) 0%, transparent 60%)"
                    : "radial-gradient(circle, rgba(168,85,247,0.07) 0%, transparent 60%)",
                }}
              />
              {/* Dashboard window */}
              <div
                className="relative rounded-xl overflow-hidden shadow-2xl"
                style={{
                  background: isDark ? "#111119" : "#ffffff",
                  boxShadow: isDark
                    ? "0 25px 50px -12px rgba(0,0,0,0.5), 0 0 0 1px rgba(255,255,255,0.05)"
                    : "0 25px 50px -12px rgba(0,0,0,0.15), 0 0 0 1px rgba(0,0,0,0.08)",
                }}
              >
                <DispatchContent isDark={isDark} progress={dispProgress} />
              </div>
            </div>
          </div>
        </FadeInStep>
      </div>
    </section>
  );
}
