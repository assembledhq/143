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
                143 started as an internal project at{" "}
                <a
                  href="https://www.assembled.com"
                  target="_blank"
                  rel="noopener noreferrer"
                  className={`underline underline-offset-2 ${isDark ? "hover:text-white/70" : "hover:text-slate-800"} transition-colors`}
                >
                  Assembled
                </a>
                . We wanted to speed up our engineering team and looked at
                everything out there. None of the existing tools had what we
                needed: agentic thinking, cloud-based coding, and a way for
                non-technical people to build product too.
              </p>
            </section>

            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                Built for ourselves first
              </h2>
              <p>
                So we built it ourselves. 143 is an autopilot for coding agents
                that lets anyone on the team spin up a cloud session, describe
                what they need, and watch it get built. Engineers use it to move
                faster day to day. PMs and designers use it to prototype features
                and ship fixes without waiting on a sprint.
              </p>
              <p>
                The name comes from the 143 days it took a small team of
                Lockheed engineers to build America&apos;s first jet fighter in
                1943 &mdash;{" "}
                <Link
                  href="/why-143"
                  className={`underline underline-offset-2 ${isDark ? "hover:text-white/70" : "hover:text-slate-800"} transition-colors`}
                >
                  the story behind the name
                </Link>
                .
              </p>
            </section>

            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                Open source from day one
              </h2>
              <p>
                We built 143 as open source from the start. Our founders have
                been in open source for a long time and always wanted to
                contribute back to the community. 143 was that chance. We think
                the best developer tools are built in the open and shaped by the
                people who use them. We hope it helps other teams move faster and
                bring more people into the building process.
              </p>
            </section>

            <section className="space-y-3">
              <h2
                className={`text-base font-medium ${isDark ? "text-white/70" : "text-slate-800"}`}
              >
                One platform, every model, every agent
              </h2>
              <p>
                Every model provider only supports their own models. That
                frustrated us. We wanted one place where different people on the
                team could use whatever coding agent they preferred, whether
                that&apos;s Claude, GPT, Gemini, or something else, without getting
                locked into a single vendor.
              </p>
              <p>
                It&apos;s not just about model choice though. We wanted a single
                experience for the whole team where you can see all coding agent
                usage in one place, collaborate on projects that span multiple
                PRs and commits, and keep context alive across sessions instead
                of starting from scratch every time.
              </p>
              <p>
                We also wanted to build a PM agent that actually knows your
                company&apos;s context. Your codebase, your product, your priorities.
                One that keeps building on that knowledge over time and gets
                smarter the more your team uses it.
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
                deliver better customer experiences. 143 is a piece of the
                engineering culture behind Assembled that we wanted to share with
                everyone.
              </p>
            </section>
          </div>
        </div>
      </main>

      <Footer isDark={isDark} />
    </div>
  );
}
