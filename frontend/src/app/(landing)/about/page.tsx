"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import Link from "next/link";
import Footer from "@/components/landing/footer";

export default function AboutPage() {
  const isDark = usePrefersDark();
  const navLinkClass = `text-sm font-medium ${
    isDark ? "text-white/60 hover:text-white" : "text-slate-600 hover:text-slate-900"
  } transition-colors`;
  const pageTitleClass = `text-2xl sm:text-3xl font-light tracking-tight ${
    isDark ? "text-white" : "text-slate-900"
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
            <h1 className={pageTitleClass}>Why we built 143.dev</h1>
            <div className="space-y-1">
              <p className={metaClass}>
                <a
                  href="https://www.linkedin.com/in/johnjianwang/"
                  target="_blank"
                  rel="noopener noreferrer"
                  className={linkClass}
                >
                  John Wang
                </a>
              </p>
              <p className={metaClass}>Co-founder &amp; CTO, Assembled</p>
            </div>
          </header>

          <div className={ruleClass} aria-hidden="true" />

          <div className={bodyClass}>
            <p>
              The best person to understand a problem really deeply usually isn&apos;t an
              engineer, it&apos;s usually someone who&apos;s using the product day in and day out
              with customers. Or it&apos;s the customer support person who sees questions all day
              about why a particular feature isn&apos;t working. While engineers have historically
              been the only people who could fix things, that&apos;s not true anymore.
            </p>
            <p>
              Now with coding agents, non-engineers can fix things too and tend to be closer to the
              problems that users run into on a daily basis. The problem is that the tools built on top of these
              agents weren&apos;t made for that person, they were built for engineers by engineers.
              That&apos;s why we built 143.
            </p>

            <section className="space-y-3 pt-2">
              <h2 className={headingClass}>Where it started</h2>
              <div className="space-y-6">
                <p>
                  At Assembled, we saw this firsthand: our support and product teams kept surfacing
                  fixes that engineers never had time for. Coding agents could have handled many of
                  them, if the tooling didn&apos;t assume you lived in a terminal.
                </p>
                <p>
                  143.dev is the internal coding agent infrastructure we built at{" "}
                  <a
                    href="https://www.assembled.com"
                    target="_blank"
                    rel="noopener noreferrer"
                    className={linkClass}
                  >
                    Assembled
                  </a>{" "}
                  to help our non-engineers with this problem (while also helping our engineers build better software). We wanted coding
                  agents to help with real product work, not just demos and
                  internal tools. 
                </p>
              </div>
            </section>

            <section className="space-y-3 pt-2">
              <h2 className={headingClass}>What we built</h2>
              <div className="space-y-6">
                <p>
                  We started with a small tiger team that cleaned up our
                  instructions, invested more in CI/CD, built agent hooks, and
                  made the agent environment less fragile. All of that helped,
                  but it also made the bigger issue obvious: we needed a system
                  that made this work shared across the team as opposed to being
                  trapped inside each engineer&apos;s terminal.
                </p>

                <p>
                  We were inspired by internal
                  systems like Stripe Minions and Ramp Inspect, but those were
                  never available to the public. We wanted something open source
                  that other teams could use, adapt, and improve.
                </p>

                <p>
                  We built 143 so the person who spots the bug doesn&apos;t need to become an
                  engineer to fix it. That meant:
                </p>

                <ul className="list-disc space-y-2 pl-5">
                  <li>
                    Automations shouldn&apos;t be hidden on one engineer&apos;s laptop, so anyone on
                    the team can see what&apos;s running and what changed.
                  </li>
                  <li>
                    Teams should be able to swap out intelligence and harnesses
                    as coding agents and models improve.
                  </li>
                  <li>
                    Shared context should make it natural to start work automatically from
                    Sentry issues, Linear assignments, PR comments, or scheduled
                    checks.
                  </li>
                  <li>
                    Code review should be handled by agents on some or all PRs,
                    and they should be able to auto-approve low-risk changes
                    against thresholds you define.
                  </li>
                  <li>
                    You should be able to set up a great environment once for
                    everyone, with the same repos, credentials, tools, logs,
                    docs, and product context available to the whole team.
                  </li>
                </ul>
              </div>
            </section>

            <section className="space-y-3 pt-2">
              <h2 className={headingClass}>Open source for everyone</h2>
              <p>
                The same idea that you shouldn&apos;t have to be an insider to contribute is why we
                open-sourced 143.
              </p>
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
                of the codebase, and eventually got really deep into the internals of Active Record. That
                work helped me get my job at Stripe and became the launching pad
                for the rest of my career.
              </p>
              <p>
                I want 143 to be available in that same spirit. I hope it helps
                other people and teams the way open source helped me.
              </p>
            </section>

            <p>I really hope you like it.</p>
          </div>
        </article>
      </main>

      <Footer isDark={isDark} />
    </div>
  );
}
