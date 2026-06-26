"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import Link from "next/link";
import Footer from "@/components/landing/footer";
import type { ReactNode } from "react";

export default function AboutPage() {
  const isDark = usePrefersDark();
  const navLinkClass = `text-sm font-medium ${
    isDark ? "text-white/60 hover:text-white" : "text-slate-600 hover:text-slate-900"
  } transition-colors`;
  const pageTitleClass = `text-2xl sm:text-3xl font-light tracking-tight ${
    isDark ? "text-white" : "text-slate-900"
  }`;
  const ledeClass = `max-w-2xl text-base leading-7 ${
    isDark ? "text-white/58" : "text-slate-600"
  }`;
  const bodyClass = `space-y-6 text-sm leading-7 ${
    isDark ? "text-white/58" : "text-slate-600"
  }`;
  const headingClass = `text-base font-medium ${
    isDark ? "text-white/78" : "text-slate-800"
  }`;
  const metaClass = `text-xs font-medium uppercase tracking-wider ${
    isDark ? "text-white/35" : "text-slate-500"
  }`;
  const linkClass = `underline underline-offset-2 ${
    isDark ? "hover:text-white/78" : "hover:text-slate-800"
  } transition-colors`;
  const ruleClass = `my-8 h-px w-full ${
    isDark ? "bg-white/10" : "bg-slate-900/12"
  }`;

  return (
    <div
      className="min-h-screen flex flex-col"
      style={{ background: isDark ? "#08080f" : "#d4e6f5" }}
    >
      <div className="flex items-center justify-between px-6 sm:px-10 pt-6 sm:pt-8">
        <Link href="/" className={navLinkClass}>
          &larr; 143
        </Link>
      </div>

      <main className="flex-1 px-6 sm:px-10 py-14 sm:py-20">
        <article aria-label="Why we built 143" className="mx-auto max-w-[720px]">
          <header className="space-y-5">
            <p className={metaClass}>143.dev</p>
            <h1 className={pageTitleClass}>Why we built 143</h1>
            <p className={ledeClass}>
              Coding agents feel magical in a fresh repo and fall apart in a
              complex codebase. The hard part was never the prompt, it&apos;s
              everything around the agent from setup to context, CI/CD, credentials,
              review, and handoff. 143.dev is an open-source project that
              helps teams actually ship production code with agents.
            </p>
          </header>

          <div className={ruleClass} aria-hidden="true" />

          <div className={bodyClass}>
            <p>
              I hate when people say &ldquo;X% of our code is written by AI.&rdquo; 
              Code volume, especially these days, is a bad
              metric. What I care about when I&apos;m on a team is whether we&apos;re building a better
              product that is genuinely useful to our customers. That is a much
              harder thing to measure, but I think it is the right one.
            </p>

            <p>
              I don&apos;t want to write AI slop. But I do want and need our team to move faster
              and compete in the market. So we set out to build a system that
              speeds up how quickly we can ship code meant for
              production.
            </p>

            <p>
              143 started at{" "}
              <a
                href="https://www.assembled.com"
                target="_blank"
                rel="noopener noreferrer"
                className={linkClass}
              >
                Assembled
              </a>
              . We wanted coding agents to help with real product work, not just
              demos and internal tools. But even heading into 2026, we weren&apos;t
              seeing the velocity gains we expected. Other teams seemed to be
              getting huge leverage from fresh repos, while we were still losing
              time to setup, context, CI, review, and handoff.
            </p>

            <p>
              We kept asking what we were doing wrong. After talking to a lot of
              other teams, the answer got much clearer: this was a shared
              infrastructure problem, not just an individual prompting problem.
            </p>

            <InlineCallout isDark={isDark} label="What was missing">
              The useful unit was not one better prompt. It was everything
              around the agent: setup, context, CI, credentials, logs, review,
              handoff, and the product knowledge needed to make good changes.
            </InlineCallout>

            <p>
              So we started with a small tiger team. We cleaned up our
              instructions, invested more in CI/CD, built agent hooks, and made
              the agent environment less fragile. All of that helped, but
              it also made the bigger issue obvious: we needed a system that made
              this work shared across the team as opposed to being trapped inside one
              engineer&apos;s terminal.
            </p>

            <p>
              That is why we built 143.dev. We were inspired by internal systems
              like Stripe Minions and Ramp Inspect, but those were never
              available to the public. We wanted something open source that other
              teams could use, adapt, and improve.
            </p>

            <p>We built around a few convictions:</p>

            <ul className="list-disc space-y-2 pl-5">
              <li>
                Automations should be visible to the team, not hidden on one
                person&apos;s laptop.
              </li>
              <li>
                Teams should be able to swap out intelligence and harnesses as coding agents
                and models improve.
              </li>
              <li>
                Hooks should make it natural to start work automatically from Sentry issues,
                Linear assignments, PR comments, or scheduled checks.
              </li>
              <li>
                Code review should be handled by agents on some or all PRs, and
                they should be able to auto-approve low-risk changes against
                thresholds you define.
              </li>
              <li>
                You should be able to set up a great environment once for
                everyone, with the same repos, credentials, tools, logs, docs,
                and product context available to the whole team.
              </li>
            </ul>

            <section className="space-y-3 pt-2">
              <h2 className={headingClass}>Open source from day one</h2>
              <p>
                I owe a lot of my career to early open-source work on Ruby on
                Rails. That is where I learned software fundamentals from people
                like Aaron Patterson, Santiago Pastorino, Jose Valim, and
                Jeremy Doerr. Their PR reviews, their patience, and their
                willingness to design-pair with strangers on the internet shaped
                how I think about software.
              </p>
              <p>
                I was just a college student, but the Rails core team
                didn&apos;t care who I was. If a PR was good and well-intentioned, it was
                welcome. I started with tests and tiny refactors, learned more
                of the codebase, and eventually fixed Active Record bugs. That
                work helped me get my job at Stripe and became the launching pad
                for the rest of my career.
              </p>
              <p>
                I want 143 to be available in that same spirit. I hope it helps
                other people and teams the way open source helped me.
              </p>
            </section>

            <p>
              <Link href="/why-143" className={linkClass}>
                The name for 143 comes from
              </Link>{" "}
              1943, when the Lockheed Skunk Works team built the XP-80 Shooting
              Star in 143 days. It is a nod to what it takes to move that fast: a small
              team with high ownership and the infrastructure to support it.
            </p>

            <p>I really hope you like it.</p>

            <div className="space-y-1">
              <p className={isDark ? "text-white/78" : "text-slate-800"}>John</p>
              <p className={metaClass}>
                <a
                  href="https://www.linkedin.com/in/johnjianwang/"
                  target="_blank"
                  rel="noopener noreferrer"
                  className={linkClass}
                >
                  Co-founder/CTO, Assembled
                </a>
              </p>
            </div>
          </div>
        </article>
      </main>

      <Footer isDark={isDark} />
    </div>
  );
}

function InlineCallout({
  isDark,
  label,
  children,
}: {
  isDark: boolean;
  label: string;
  children: ReactNode;
}) {
  const calloutClass = `my-7 border-l py-1 pl-5 ${
    isDark ? "border-white/16 text-white/64" : "border-slate-900/18 text-slate-600"
  }`;
  const labelClass = `mb-2 text-xs font-medium uppercase tracking-wider ${
    isDark ? "text-white/42" : "text-slate-500"
  }`;

  return (
    <aside role="note" aria-label={label} className={calloutClass}>
      <p className={labelClass}>{label}</p>
      <p className="text-sm leading-7">{children}</p>
    </aside>
  );
}
