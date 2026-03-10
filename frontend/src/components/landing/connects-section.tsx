"use client";

import RadarCanvas from "./radar-canvas";

interface ConnectsSectionProps {
  isDark: boolean;
}

export default function ConnectsSection({ isDark }: ConnectsSectionProps) {
  return (
    <section
      className="relative py-24 sm:py-32 px-6 sm:px-10"
      style={{ background: isDark ? "#08080f" : "#f0f4f8" }}
    >
      <div className="mx-auto max-w-6xl flex flex-col md:flex-row items-center gap-12 md:gap-16">
        {/* LEFT: HTML content */}
        <div className="flex-1 space-y-5">
          <p
            className={`text-xs font-mono tracking-widest uppercase ${
              isDark ? "text-white/30" : "text-slate-400"
            }`}
          >
            Step 01
          </p>
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

        {/* RIGHT: Radar canvas */}
        <div className="flex-1 w-full max-w-[500px] aspect-square rounded-lg overflow-hidden">
          <RadarCanvas isDark={isDark} />
        </div>
      </div>
    </section>
  );
}
