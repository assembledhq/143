"use client";

import Image from "next/image";
import { Card, CardContent } from "@/components/ui/card";
import { codingAgents, integrations } from "./landing-copy";
import { landingTypography as type } from "./landing-typography";

interface IntegrationsSectionProps {
  isDark: boolean;
}

interface ToolItem {
  name: string;
  logo: string;
  description: string;
}

export default function IntegrationsSection({
  isDark,
}: IntegrationsSectionProps) {
  const label = isDark ? "text-white/30" : "text-slate-500";
  const heading = isDark ? "text-white" : "text-slate-900";
  const body = isDark ? "text-white/50" : "text-slate-600";

  const renderGrid = (items: ToolItem[]) => (
    <div className="grid gap-px overflow-hidden rounded-lg border border-border/60 bg-border/60 sm:grid-cols-2 lg:grid-cols-3">
      {items.map((item) => (
        <Card
          key={item.name}
          className={`min-h-44 rounded-none border-0 ${
            isDark ? "bg-[#0d0d15]" : "bg-white/80"
          }`}
        >
          <CardContent className="p-5">
            <div className="flex items-center gap-3">
              <div
                className={`flex size-10 items-center justify-center rounded-md border ${
                  isDark
                    ? "border-white/10 bg-white/5"
                    : "border-slate-200 bg-white"
                }`}
              >
                <Image
                  src={item.logo}
                  alt={`${item.name} logo`}
                  width={22}
                  height={22}
                />
              </div>
              <h3
                className={`${type.cardTitle} ${
                  isDark ? "text-white/85" : "text-slate-900"
                }`}
              >
                {item.name}
              </h3>
            </div>
            <p className={`mt-5 ${type.body} ${body}`}>{item.description}</p>
          </CardContent>
        </Card>
      ))}
    </div>
  );

  return (
    <section
      id="integrations"
      className="relative overflow-hidden px-6 py-28 sm:px-10 sm:py-36"
      style={{ background: isDark ? "#0a0a12" : "#f2f5f9" }}
    >
      <div className="mx-auto max-w-5xl">
        <div className="grid gap-8 lg:grid-cols-[0.38fr_0.62fr] lg:items-end">
          <p className={`${type.eyebrow} ${label}`}>07 Coding agents</p>
          <div className="space-y-5">
            <h2 className={`max-w-3xl ${type.featureTitle} ${heading}`}>
              Run any coding agent.
            </h2>
            <p className={`max-w-2xl ${type.body} ${body}`}>
              Bring the agent your team already trusts. Connect its auth once and
              start runs across web, mobile, Slack, Linear, and Sentry.
            </p>
          </div>
        </div>

        <div className="mt-14">{renderGrid(codingAgents)}</div>

        <div className="mt-24 grid gap-8 lg:grid-cols-[0.38fr_0.62fr] lg:items-end">
          <p className={`${type.eyebrow} ${label}`}>08 Integrations</p>
          <div className="space-y-5">
            <h2 className={`max-w-3xl ${type.featureTitle} ${heading}`}>
              Connect your engineering tools.
            </h2>
            <p className={`max-w-2xl ${type.body} ${body}`}>
              Integrations are configured once for the organization, so every
              agent starts with the same source of truth your team uses every
              day.
            </p>
          </div>
        </div>

        <div className="mt-14">{renderGrid(integrations)}</div>
      </div>
    </section>
  );
}
