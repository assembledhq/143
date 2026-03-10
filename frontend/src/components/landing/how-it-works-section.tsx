"use client";

import RadarCanvas from "./radar-canvas";

interface HowItWorksSectionProps {
  isDark: boolean;
}

const dispatches = [
  {
    agent: "Claude Code",
    task: "Fix null ref in auth flow",
    source: "Sentry",
    status: "PR open",
    statusColor: "green",
  },
  {
    agent: "Codex",
    task: "Add session store schema",
    source: "Project",
    status: "Building",
    statusColor: "blue",
  },
  {
    agent: "Gemini CLI",
    task: "Update deprecated API calls",
    source: "Tech Debt",
    status: "CI running",
    statusColor: "yellow",
  },
];

export default function HowItWorksSection({ isDark }: HowItWorksSectionProps) {
  const stepLabel = (num: string) => (
    <p
      className={`text-xs font-mono tracking-widest uppercase mb-5 ${
        isDark ? "text-white/30" : "text-slate-400"
      }`}
    >
      Step {num}
    </p>
  );

  return (
    <section
      className="relative py-24 sm:py-32 px-6 sm:px-10"
      style={{ background: isDark ? "#0a0a12" : "#f2f5f9" }}
    >
      <div className="mx-auto max-w-5xl space-y-24 sm:space-y-32">
        {/* ── Step 01: Everything Connects ── text LEFT, radar RIGHT */}
        <div className="flex flex-col md:flex-row items-center gap-12 md:gap-16">
          <div className="flex-1 space-y-5">
            {stepLabel("01")}
            <h2
              className={`text-3xl sm:text-4xl font-light tracking-tight ${
                isDark ? "text-white" : "text-slate-900"
              }`}
            >
              Everything Connects
            </h2>
            <p
              className={`text-sm sm:text-base leading-relaxed max-w-md ${
                isDark ? "text-white/50" : "text-slate-600"
              }`}
            >
              Sentry errors, Linear tickets, support threads, and your product
              roadmap &mdash; the PM sees your entire production surface in one
              place.
            </p>
            <ul
              className={`space-y-2 text-sm ${
                isDark ? "text-white/40" : "text-slate-500"
              }`}
            >
              {[
                "Bugs and errors from Sentry",
                "Tickets and projects from Linear",
                "Customer reports from support channels",
                "Your product roadmap and priorities",
              ].map((item) => (
                <li key={item} className="flex items-start gap-2">
                  <span
                    className={`mt-1.5 h-1 w-1 rounded-full flex-shrink-0 ${
                      isDark ? "bg-green-400/60" : "bg-green-600/60"
                    }`}
                  />
                  {item}
                </li>
              ))}
            </ul>
          </div>
          <div className="flex-1 w-full max-w-[500px] aspect-square rounded-lg overflow-hidden">
            <RadarCanvas isDark={isDark} />
          </div>
        </div>

        {/* ── Step 02: Always On ── loop LEFT, text RIGHT (flipped) */}
        <div className="flex flex-col md:flex-row-reverse items-start gap-10 md:gap-16">
          <div className="md:w-2/5 flex-shrink-0">
            {stepLabel("02")}
            <div
              className={`inline-flex items-center gap-2 rounded-full px-3 py-1 text-xs font-mono mb-5 ${
                isDark
                  ? "bg-green-400/10 text-green-400/80 border border-green-400/20"
                  : "bg-green-50 text-green-700 border border-green-200"
              }`}
            >
              <span className="relative flex h-2 w-2">
                <span
                  className={`animate-ping absolute inline-flex h-full w-full rounded-full opacity-75 ${
                    isDark ? "bg-green-400" : "bg-green-500"
                  }`}
                />
                <span
                  className={`relative inline-flex rounded-full h-2 w-2 ${
                    isDark ? "bg-green-400" : "bg-green-500"
                  }`}
                />
              </span>
              Always on
            </div>
            <h3
              className={`text-xl sm:text-2xl font-light tracking-tight mb-4 ${
                isDark ? "text-white" : "text-slate-900"
              }`}
            >
              Wakes up. Fixes bugs.
              <br />
              Goes back to sleep.
            </h3>
            <p
              className={`text-sm leading-relaxed max-w-sm ${
                isDark ? "text-white/45" : "text-slate-600"
              }`}
            >
              Every few hours, the PM scans your production surface &mdash;
              Sentry errors, Linear tickets, support threads. It picks the
              highest-impact issues, dispatches your coding agents to fix them,
              and ships validated PRs. No prompt needed.
            </p>
          </div>
          <div className="md:w-3/5">
            <div
              className={`rounded-lg border p-6 sm:p-8 ${
                isDark
                  ? "bg-white/[0.02] border-white/[0.06]"
                  : "bg-white border-slate-200"
              }`}
            >
              <div className="flex flex-col gap-4">
                {[
                  {
                    step: "SCAN",
                    detail: "3 new Sentry errors, 2 Linear tickets",
                    accent: isDark ? "text-yellow-400/70" : "text-yellow-600",
                  },
                  {
                    step: "PRIORITIZE",
                    detail: "TypeError in auth flow \u2192 highest impact",
                    accent: isDark ? "text-orange-400/70" : "text-orange-600",
                  },
                  {
                    step: "DISPATCH",
                    detail: "Agent fixing null ref in handleLogin()",
                    accent: isDark ? "text-blue-400/70" : "text-blue-600",
                  },
                  {
                    step: "VALIDATE",
                    detail: "CI passing \u2022 47/47 tests \u2022 PR ready",
                    accent: isDark ? "text-green-400/70" : "text-green-600",
                  },
                ].map((item, i) => (
                  <div key={item.step} className="flex items-start gap-4">
                    <div className="flex flex-col items-center flex-shrink-0">
                      <div
                        className={`w-8 h-8 rounded-full flex items-center justify-center text-xs font-mono font-bold ${
                          isDark
                            ? "bg-white/[0.06] text-white/50"
                            : "bg-slate-100 text-slate-500"
                        }`}
                      >
                        {i + 1}
                      </div>
                      {i < 3 && (
                        <div
                          className={`w-px h-4 ${
                            isDark ? "bg-white/[0.08]" : "bg-slate-200"
                          }`}
                        />
                      )}
                    </div>
                    <div className="pt-1">
                      <span
                        className={`text-xs font-mono font-bold tracking-wider ${item.accent}`}
                      >
                        {item.step}
                      </span>
                      <p
                        className={`text-sm mt-0.5 ${
                          isDark ? "text-white/40" : "text-slate-500"
                        }`}
                      >
                        {item.detail}
                      </p>
                    </div>
                  </div>
                ))}
                {/* Loop arrow */}
                <div className="flex items-center gap-4">
                  <div className="w-8 flex-shrink-0 flex justify-center">
                    <svg
                      width="16"
                      height="16"
                      viewBox="0 0 16 16"
                      fill="none"
                      className={isDark ? "text-white/20" : "text-slate-300"}
                    >
                      <path
                        d="M8 2v10M4 8l4 4 4-4"
                        stroke="currentColor"
                        strokeWidth="1.5"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                      />
                    </svg>
                  </div>
                  <p
                    className={`text-xs font-mono ${
                      isDark ? "text-white/20" : "text-slate-400"
                    }`}
                  >
                    repeats every few hours
                  </p>
                </div>
              </div>
            </div>
          </div>
        </div>

        {/* ── Step 03: You Direct ── text LEFT, project RIGHT */}
        <div className="flex flex-col md:flex-row items-start gap-10 md:gap-16">
          <div className="md:w-2/5 flex-shrink-0">
            {stepLabel("03")}
            <div
              className={`inline-flex items-center gap-2 rounded-full px-3 py-1 text-xs font-mono mb-5 ${
                isDark
                  ? "bg-blue-400/10 text-blue-400/80 border border-blue-400/20"
                  : "bg-blue-50 text-blue-700 border border-blue-200"
              }`}
            >
              You direct
            </div>
            <h3
              className={`text-xl sm:text-2xl font-light tracking-tight mb-4 ${
                isDark ? "text-white" : "text-slate-900"
              }`}
            >
              Create projects.
              <br />
              The PM chips away.
            </h3>
            <p
              className={`text-sm leading-relaxed max-w-sm ${
                isDark ? "text-white/45" : "text-slate-600"
              }`}
            >
              Migrations, refactors, new features &mdash; define the big
              initiative and the PM breaks it into sequenced tasks. Set
              priorities in plain language and it balances across bugs, projects,
              and tech debt.
            </p>
          </div>
          <div className="md:w-3/5">
            <div
              className={`rounded-lg border p-6 sm:p-8 ${
                isDark
                  ? "bg-white/[0.02] border-white/[0.06]"
                  : "bg-white border-slate-200"
              }`}
            >
              <div className="space-y-4">
                <div>
                  <span
                    className={`text-xs font-mono tracking-wider ${
                      isDark ? "text-blue-400/60" : "text-blue-600"
                    }`}
                  >
                    PROJECT
                  </span>
                  <p
                    className={`text-sm font-medium mt-1 ${
                      isDark ? "text-white/80" : "text-slate-800"
                    }`}
                  >
                    Migrate auth from JWT to session tokens
                  </p>
                </div>
                <div
                  className={`h-px ${
                    isDark ? "bg-white/[0.06]" : "bg-slate-100"
                  }`}
                />
                <div className="space-y-3">
                  {[
                    { task: "Add session store schema", status: "done" },
                    { task: "Update login endpoint", status: "done" },
                    {
                      task: "Migrate middleware to session checks",
                      status: "active",
                    },
                    { task: "Remove JWT dependencies", status: "pending" },
                    { task: "Update integration tests", status: "pending" },
                  ].map((item) => (
                    <div key={item.task} className="flex items-center gap-3">
                      <span className="flex-shrink-0">
                        {item.status === "done" ? (
                          <span
                            className={`text-sm ${
                              isDark ? "text-green-400/70" : "text-green-600"
                            }`}
                          >
                            &#10003;
                          </span>
                        ) : item.status === "active" ? (
                          <span className="relative flex h-2 w-2 ml-0.5">
                            <span
                              className={`animate-ping absolute inline-flex h-full w-full rounded-full opacity-75 ${
                                isDark ? "bg-blue-400" : "bg-blue-500"
                              }`}
                            />
                            <span
                              className={`relative inline-flex rounded-full h-2 w-2 ${
                                isDark ? "bg-blue-400" : "bg-blue-500"
                              }`}
                            />
                          </span>
                        ) : (
                          <span
                            className={`inline-flex h-2 w-2 ml-0.5 rounded-full ${
                              isDark ? "bg-white/10" : "bg-slate-200"
                            }`}
                          />
                        )}
                      </span>
                      <span
                        className={`text-sm font-mono ${
                          item.status === "done"
                            ? isDark
                              ? "text-white/30 line-through"
                              : "text-slate-400 line-through"
                            : item.status === "active"
                              ? isDark
                                ? "text-white/70"
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
                <p
                  className={`text-xs font-mono pt-2 ${
                    isDark ? "text-white/20" : "text-slate-400"
                  }`}
                >
                  &ldquo;focus on auth this sprint&rdquo; &rarr; PM rebalances
                  automatically
                </p>
              </div>
            </div>
          </div>
        </div>

        {/* ── Step 04: Your Agents Execute ── board LEFT, text RIGHT (flipped) */}
        <div className="flex flex-col md:flex-row-reverse items-start gap-10 md:gap-16">
          <div className="md:w-2/5 flex-shrink-0">
            {stepLabel("04")}
            <h2
              className={`text-2xl sm:text-3xl font-light tracking-tight mb-4 ${
                isDark ? "text-white" : "text-slate-900"
              }`}
            >
              Each engineer picks
              <br />
              their own agent
            </h2>
            <p
              className={`text-sm sm:text-base leading-relaxed max-w-sm ${
                isDark ? "text-white/45" : "text-slate-600"
              }`}
            >
              Claude Code, Codex, Gemini CLI &mdash; or any agent your team
              already runs. The PM doesn&rsquo;t care which agent writes the
              code. It dispatches the task, your agent builds it, your CI
              validates it.
            </p>
            <p
              className={`text-sm leading-relaxed max-w-sm mt-4 ${
                isDark ? "text-white/30" : "text-slate-500"
              }`}
            >
              Different engineers can use different agents on the same team. The
              PM coordinates the work, not the tooling.
            </p>
          </div>
          <div className="md:w-3/5">
            <div
              className={`rounded-lg border overflow-hidden ${
                isDark
                  ? "bg-white/[0.02] border-white/[0.06]"
                  : "bg-white border-slate-200"
              }`}
            >
              {/* Header */}
              <div
                className={`px-5 py-3 border-b ${
                  isDark
                    ? "border-white/[0.06] bg-white/[0.02]"
                    : "border-slate-100 bg-slate-50"
                }`}
              >
                <div className="flex items-center gap-2">
                  <span className="relative flex h-2 w-2">
                    <span
                      className={`animate-ping absolute inline-flex h-full w-full rounded-full opacity-75 ${
                        isDark ? "bg-green-400" : "bg-green-500"
                      }`}
                    />
                    <span
                      className={`relative inline-flex rounded-full h-2 w-2 ${
                        isDark ? "bg-green-400" : "bg-green-500"
                      }`}
                    />
                  </span>
                  <span
                    className={`text-xs font-mono tracking-wider ${
                      isDark ? "text-white/40" : "text-slate-500"
                    }`}
                  >
                    DISPATCH BOARD
                  </span>
                </div>
              </div>

              {/* Rows */}
              <div
                className={`divide-y ${
                  isDark ? "divide-white/[0.04]" : "divide-slate-100"
                }`}
              >
                {dispatches.map((d) => {
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
                  const colors =
                    statusColors[d.statusColor as keyof typeof statusColors];

                  return (
                    <div key={d.agent} className="px-5 py-4">
                      <div className="flex flex-col sm:flex-row sm:items-center gap-2 sm:gap-4">
                        <div className="sm:w-32 flex-shrink-0">
                          <span
                            className={`text-sm font-mono font-medium ${
                              isDark ? "text-white/70" : "text-slate-800"
                            }`}
                          >
                            {d.agent}
                          </span>
                        </div>
                        <div className="flex-1 min-w-0">
                          <p
                            className={`text-sm truncate ${
                              isDark ? "text-white/40" : "text-slate-600"
                            }`}
                          >
                            {d.task}
                          </p>
                          <span
                            className={`text-xs font-mono ${
                              isDark ? "text-white/20" : "text-slate-400"
                            }`}
                          >
                            via {d.source}
                          </span>
                        </div>
                        <div className="flex-shrink-0">
                          <span
                            className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-mono ${colors}`}
                          >
                            {d.status}
                          </span>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>

              {/* Footer */}
              <div
                className={`px-5 py-3 border-t ${
                  isDark
                    ? "border-white/[0.04] bg-white/[0.01]"
                    : "border-slate-100 bg-slate-50/50"
                }`}
              >
                <p
                  className={`text-xs font-mono ${
                    isDark ? "text-white/20" : "text-slate-400"
                  }`}
                >
                  3 agents active &middot; 2 PRs shipped today
                </p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
