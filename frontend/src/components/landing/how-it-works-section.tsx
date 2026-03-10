"use client";

interface HowItWorksSectionProps {
  isDark: boolean;
}

export default function HowItWorksSection({ isDark }: HowItWorksSectionProps) {
  return (
    <section
      className="relative py-24 sm:py-32 px-6 sm:px-10"
      style={{ background: isDark ? "#0c0c14" : "#f5f7fa" }}
    >
      <div className="mx-auto max-w-5xl">
        <h2
          className={`text-2xl sm:text-3xl font-light tracking-tight mb-16 ${
            isDark ? "text-white" : "text-slate-900"
          }`}
        >
          How It Works
        </h2>

        <div className="space-y-20">
          {/* Mode 1: The Loop */}
          <div className="flex flex-col md:flex-row gap-10 md:gap-16">
            <div className="md:w-2/5 flex-shrink-0">
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
                highest-impact issues, dispatches your coding agents to fix
                them, and ships validated PRs. No prompt needed.
              </p>
            </div>
            <div className="md:w-3/5">
              {/* The loop visualization */}
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
                      accent: isDark
                        ? "text-yellow-400/70"
                        : "text-yellow-600",
                    },
                    {
                      step: "PRIORITIZE",
                      detail:
                        "TypeError in auth flow \u2192 highest impact",
                      accent: isDark
                        ? "text-orange-400/70"
                        : "text-orange-600",
                    },
                    {
                      step: "DISPATCH",
                      detail: "Agent fixing null ref in handleLogin()",
                      accent: isDark
                        ? "text-blue-400/70"
                        : "text-blue-600",
                    },
                    {
                      step: "VALIDATE",
                      detail: "CI passing \u2022 47/47 tests \u2022 PR ready",
                      accent: isDark
                        ? "text-green-400/70"
                        : "text-green-600",
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
                        className={
                          isDark ? "text-white/20" : "text-slate-300"
                        }
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

          {/* Divider */}
          <div
            className={`h-px ${isDark ? "bg-white/[0.06]" : "bg-slate-200"}`}
          />

          {/* Mode 2: Projects */}
          <div className="flex flex-col md:flex-row gap-10 md:gap-16">
            <div className="md:w-2/5 flex-shrink-0">
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
                priorities in plain language and it balances across bugs,
                projects, and tech debt.
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
                {/* Project example */}
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
                      {
                        task: "Add session store schema",
                        status: "done",
                      },
                      {
                        task: "Update login endpoint",
                        status: "done",
                      },
                      {
                        task: "Migrate middleware to session checks",
                        status: "active",
                      },
                      { task: "Remove JWT dependencies", status: "pending" },
                      { task: "Update integration tests", status: "pending" },
                    ].map((item) => (
                      <div
                        key={item.task}
                        className="flex items-center gap-3"
                      >
                        <span className="flex-shrink-0">
                          {item.status === "done" ? (
                            <span
                              className={`text-sm ${
                                isDark
                                  ? "text-green-400/70"
                                  : "text-green-600"
                              }`}
                            >
                              &#10003;
                            </span>
                          ) : item.status === "active" ? (
                            <span className="relative flex h-2 w-2 ml-0.5">
                              <span
                                className={`animate-ping absolute inline-flex h-full w-full rounded-full opacity-75 ${
                                  isDark
                                    ? "bg-blue-400"
                                    : "bg-blue-500"
                                }`}
                              />
                              <span
                                className={`relative inline-flex rounded-full h-2 w-2 ${
                                  isDark
                                    ? "bg-blue-400"
                                    : "bg-blue-500"
                                }`}
                              />
                            </span>
                          ) : (
                            <span
                              className={`inline-flex h-2 w-2 ml-0.5 rounded-full ${
                                isDark
                                  ? "bg-white/10"
                                  : "bg-slate-200"
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
                    &ldquo;focus on auth this sprint&rdquo; &rarr; PM
                    rebalances automatically
                  </p>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
