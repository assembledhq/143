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
              Coding agents are very impressive in fresh repos, but we needed
              more in order to actually get them working well in our production
              systems across teams of engineers and non-engineers. That&apos;s why
              we built 143 and open sourced it.
            </p>
            <p className={isDark ? "text-xs text-white/35" : "text-xs text-slate-500"}>
              By John, from Assembled
            </p>
          </header>

          <div className={ruleClass} aria-hidden="true" />

          <div className={bodyClass}>
            <p>
              At{" "}
              <a
                href="https://www.assembled.com"
                target="_blank"
                rel="noopener noreferrer"
                className={linkClass}
              >
                Assembled
              </a>
              , we wanted coding agents to help with real product work, not
              just demos and internal tools. Even heading into 2026, we were not
              seeing the velocity increase we expected. Other people seemed to
              be getting huge leverage from new repos. We were still losing time
              to setup, context, CI, review, and handoff.
            </p>

            <p>
              We kept asking what we were doing wrong. After talking to a lot of
              other teams, the answer felt much clearer: this was a shared
              infrastructure problem, not just an individual prompting problem.
            </p>

            <InlineCallout isDark={isDark} label="What was missing">
              The useful unit was not one better prompt. It was everything
              around the agent: setup, context, CI, credentials, logs, review,
              handoff, and the product knowledge needed to make good changes.
            </InlineCallout>

            <p>
              We started with a small tiger team. We cleaned up our
              instructions, invested more in CI/CD, built agent hooks, and made
              the agent environment less fragile. All of that helped, but it
              also made the bigger issue obvious: we needed a system that made
              this work shared by the team, not trapped inside one
              engineer&apos;s terminal.
            </p>

            <p>
              That is why we built 143.dev. We were inspired by internal systems
              like Stripe Minions and Ramp Inspect, but those were not available
              to the public. We wanted to build something open source that other
              teams could use, adapt, and improve.
            </p>

            <p>
              Vibe coding is not the right word for what we want. I care much
              more about productionalized coding. The point is not one-off apps.
              It is helping professional engineers move faster on important
              work, and giving domain experts and non-engineers a real path to
              write real code in production systems.
            </p>

            <p>
              That last part matters a lot to me. 143 should not pretend a PM,
              designer, support leader, or operator suddenly becomes a senior
              engineer. But they should be able to turn product knowledge into a
              scoped code change, with shared context, previews, pull requests,
              CI, and review gates before anything ships.
            </p>

            <InlineCallout isDark={isDark} label="For non-engineers">
              The goal is not to route around engineers. It is to make product
              knowledge useful in code, with previews, tests, pull requests, and
              review gates before anything gets merged.
            </InlineCallout>

            <p>
              A lot of today&apos;s tools make individual engineers faster, but
              they can make the team part harder. That makes sense; many were
              built by engineers for their own workflows. Running engineering
              teams made us notice that a lot of primitives were sitting at the
              wrong level.
            </p>

            <p>
              Automations should be visible to the team, not hidden on one
              person&apos;s laptop. Teams should be able to swap out intelligence
              as coding agents and models improve. Usage should be legible by
              person, PR, issue, automation, and outcome, not just token count.
              Hooks should make it natural to start work from Sentry issues,
              Linear assignments, PR comments, or scheduled checks. And you
              should be able to set up a great environment once for everyone,
              with the same repos, credentials, tools, logs, docs, and product
              context available to the whole team.
            </p>

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
                I was just a college student, but the Rails core team did not
                care who I was. If a PR was good and well-intentioned, it was
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

            <InlineCallout isDark={isDark} label="Why open source">
              A lot of us learned by reading public code and sending small
              patches. 143 should be useful out of the box, but also easy for
              teams to inspect, adapt, and improve in the open.
            </InlineCallout>

            <p>
              I also wanted the hosted version to feel like the old days of
              developer tools, when pricing was simpler. For hosted 143, we are
              charging just for the containers you run. Bring whatever LLM
              provider or coding agent you prefer. We want you to help pay for
              the servers, not make your team pay our model markup.
            </p>

            <p>
              The name comes from 1943, when the Lockheed Skunk Works team built
              the XP-80 Shooting Star in 143 days. It is a nod to small teams
              with enough ownership and infrastructure to move quickly.
            </p>

            <p>I really hope you like it.</p>

            <p className={isDark ? "text-white/78" : "text-slate-800"}>John</p>
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
