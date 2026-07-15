"use client";

import Image from "next/image";
import { Card, CardContent } from "@/components/ui/card";
import {
  agentChoiceHighlights,
  codingAgents,
  integrations,
} from "./landing-copy";
import { landingTypography as type } from "./landing-typography";

interface IntegrationsSectionProps {
  isDark: boolean;
}

interface ToolItem {
  name: string;
  logo: string;
  description?: string;
}

export default function IntegrationsSection({
  isDark,
}: IntegrationsSectionProps) {
  const label = isDark ? "text-[#7992ff]" : "text-[#315ce8]";
  const heading = isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]";
  const body = isDark ? "text-[#aaa89f]" : "text-[#6b6b65]";

  const renderGrid = (items: ToolItem[]) => (
    <div className="flex flex-wrap justify-center gap-4">
      {items.map((item) => (
        <Card
          key={item.name}
          className={`w-full flex-none sm:w-[calc(50%_-_0.5rem)] lg:w-[calc(33.333%_-_0.667rem)] ${
            item.description ? "min-h-44" : ""
          } ${
            isDark
              ? "border-white/10 bg-[#1d1d1a]"
              : "border-[#e1ded5] bg-[#fefdfb] shadow-[0_18px_48px_-36px_rgb(36_34_28_/_32%)]"
          }`}
        >
          <CardContent className="p-6">
            <div className="flex items-center gap-3">
              <div
                className={`flex size-11 items-center justify-center rounded-lg border ${
                  isDark
                    ? "border-white/10 bg-[#242420]"
                    : "border-[#e1ded5] bg-[#f6f5f0]"
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
                  isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"
                }`}
              >
                {item.name}
              </h3>
            </div>
            {item.description && (
              <p className={`mt-5 ${type.body} ${body}`}>{item.description}</p>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  );

  return (
    <section
      id="integrations"
      className={`relative overflow-hidden border-t px-6 py-28 sm:px-10 sm:py-40 ${
        isDark ? "border-white/10" : "border-[#e1ded5]"
      }`}
      style={{ background: isDark ? "#151513" : "#f6f5f0" }}
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

        <div className="mt-16">{renderGrid(codingAgents)}</div>

        <div
          className={`mt-8 grid gap-1 rounded-2xl border p-2 md:grid-cols-3 ${
            isDark
              ? "border-white/10 bg-[#11110f]"
              : "border-[#e1ded5] bg-[#efeee8]"
          }`}
        >
          {agentChoiceHighlights.map((highlight) => (
            <Card
              key={highlight.title}
              variant="quiet"
              className={isDark ? "bg-[#1d1d1a]" : "bg-[#fefdfb]"}
            >
              <CardContent className="p-6">
                <h3
                  className={`${type.cardTitle} ${
                    isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"
                  }`}
                >
                  {highlight.title}
                </h3>
                <p className={`mt-4 ${type.body} ${body}`}>
                  {highlight.body}
                </p>
              </CardContent>
            </Card>
          ))}
        </div>

        <div className="mt-28 grid gap-8 lg:grid-cols-[0.38fr_0.62fr] lg:items-end">
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

        <div className="mt-16">{renderGrid(integrations)}</div>
      </div>
    </section>
  );
}
