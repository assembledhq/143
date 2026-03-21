"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import Link from "next/link";
import Footer from "@/components/landing/footer";

export default function AboutPage() {
  const isDark = usePrefersDark();

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
      <main className="flex-1 px-6 sm:px-10 py-16 sm:py-24">
        <div className="max-w-2xl mx-auto">
          <h1
            className={`text-2xl sm:text-3xl font-light tracking-tight mb-12 ${isDark ? "text-white" : "text-slate-900"}`}
          >
            About 143
          </h1>

          <div
            className={`space-y-8 text-sm leading-relaxed ${isDark ? "text-white/50" : "text-slate-600"}`}
          >
            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                How it started
              </h2>
              <p>
                143 began as an internal project at{" "}
                <a
                  href="https://www.assembled.com"
                  target="_blank"
                  rel="noopener noreferrer"
                  className={`underline underline-offset-2 ${isDark ? "hover:text-white/70" : "hover:text-slate-800"} transition-colors`}
                >
                  Assembled
                </a>
                . Our engineering team needed a way to move faster, and we
                looked everywhere for the right tool. Nothing quite had the
                combination we were after: agentic thinking, cloud-based coding,
                and the ability for non-technical teammates to jump in and build
                product alongside engineers.
              </p>
            </section>

            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                Built for ourselves first
              </h2>
              <p>
                So we built one. 143 started as the tool we really wanted to
                use&mdash;an autopilot for coding agents that let anyone on the
                team spin up cloud sessions, describe what they needed, and watch
                it come to life. Engineers used it to accelerate day-to-day
                development. Product managers and designers used it to prototype
                features and ship small fixes without waiting on a sprint cycle.
              </p>
              <p>
                The name comes from a simple idea: 143 means &ldquo;I love
                you&rdquo;&mdash;one letter, four letters, three letters. We
                wanted to build something we loved working with every day, and we
                did.
              </p>
            </section>

            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                Why open source
              </h2>
              <p>
                After seeing how much 143 changed the way we worked at
                Assembled, we decided to open source it and give it back to the
                community. We believe the best developer tools are built in the
                open, shaped by the people who use them. By sharing 143, we hope
                to help other teams&mdash;whether they have five engineers or
                five hundred&mdash;move faster and bring more people into the
                building process.
              </p>
            </section>

            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                About Assembled
              </h2>
              <p>
                <a
                  href="https://www.assembled.com"
                  target="_blank"
                  rel="noopener noreferrer"
                  className={`underline underline-offset-2 ${isDark ? "hover:text-white/70" : "hover:text-slate-800"} transition-colors`}
                >
                  Assembled
                </a>{" "}
                is a workforce management platform that helps support teams
                deliver better customer experiences. 143 is our way of sharing a
                piece of the engineering culture that makes Assembled
                possible&mdash;a belief that great tools should empower everyone
                to build.
              </p>
            </section>
          </div>
        </div>
      </main>

      <Footer isDark={isDark} />
    </div>
  );
}
