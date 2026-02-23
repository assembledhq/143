"use client";

import React from "react";
import { Zone } from "./zones";
import { AIRFIELD_DARK, AIRFIELD_LIGHT } from "./airfield-theme";

export interface ZoneCardProps {
  zone: Zone;
  isActive: boolean;
  zoneProgress: number;
  isDark: boolean;
  index: number;
}

export default function ZoneCard({
  zone,
  isActive,
  isDark,
}: ZoneCardProps) {
  const theme = isDark ? AIRFIELD_DARK : AIRFIELD_LIGHT;

  return (
    <div
      className="absolute pointer-events-none max-w-[280px] rounded-sm backdrop-blur-sm px-4 py-3"
      style={{
        left: "3%",
        bottom: "22%",
        transform: `translateY(${isActive ? "0" : "0.5rem"})`,
        opacity: isActive ? 1 : 0,
        backgroundColor: theme.cardBg,
        borderLeft: `2px solid ${isActive ? theme.dotActive : "transparent"}`,
        transition: "opacity 0.5s ease-out, transform 0.5s ease-out",
      }}
    >
      <div className="flex items-center gap-2 mb-1">
        <span
          className="text-[10px] font-mono tracking-[0.2em]"
          style={{ color: theme.dotActive }}
        >
          {zone.number}
        </span>
        <span
          className="text-sm font-mono font-semibold tracking-wide uppercase"
          style={{ color: theme.text }}
        >
          {zone.name}
        </span>
      </div>
      <div
        className="text-xs leading-relaxed"
        style={{ color: theme.textMuted }}
      >
        {zone.description}
      </div>
    </div>
  );
}
