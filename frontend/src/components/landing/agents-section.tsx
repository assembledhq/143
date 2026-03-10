"use client";

interface AgentsSectionProps {
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

export default function AgentsSection({ isDark }: AgentsSectionProps) {
  return (
    <section
      className="relative py-24 sm:py-32 px-6 sm:px-10"
      style={{ background: isDark ? "#08080f" : "#f0f4f8" }}
    >
      <div className="mx-auto max-w-5xl">
        <div className="flex flex-col md:flex-row gap-12 md:gap-16">
          {/* Left: content */}
          <div className="md:w-2/5 flex-shrink-0 space-y-5">
            <h2
              className={`text-2xl sm:text-3xl font-light tracking-tight ${
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
              className={`text-sm leading-relaxed max-w-sm ${
                isDark ? "text-white/30" : "text-slate-500"
              }`}
            >
              Different engineers can use different agents on the same team.
              The PM coordinates the work, not the tooling.
            </p>
          </div>

          {/* Right: dispatch board */}
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
              <div className="divide-y divide-white/[0.04]">
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
                    statusColors[
                      d.statusColor as keyof typeof statusColors
                    ];

                  return (
                    <div
                      key={d.agent}
                      className={`px-5 py-4 ${
                        isDark
                          ? "divide-white/[0.04]"
                          : "divide-slate-100"
                      }`}
                    >
                      <div className="flex flex-col sm:flex-row sm:items-center gap-2 sm:gap-4">
                        {/* Agent name */}
                        <div className="sm:w-32 flex-shrink-0">
                          <span
                            className={`text-sm font-mono font-medium ${
                              isDark ? "text-white/70" : "text-slate-800"
                            }`}
                          >
                            {d.agent}
                          </span>
                        </div>
                        {/* Task */}
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
                        {/* Status badge */}
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
