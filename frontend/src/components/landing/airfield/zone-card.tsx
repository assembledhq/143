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
  index,
}: ZoneCardProps) {
  const theme = isDark ? AIRFIELD_DARK : AIRFIELD_LIGHT;

  return (
    <div
      className="absolute pointer-events-none w-[180px] sm:w-[220px] rounded-lg backdrop-blur-md p-3"
      style={{
        left: `${zone.position.x * 100}%`,
        top: `${zone.position.y * 100}%`,
        transform: `translate(20px, -20px) translateY(${isActive ? "0" : "1rem"})`,
        opacity: isActive ? 1 : 0,
        backgroundColor: theme.cardBg,
        borderWidth: 1,
        borderStyle: "solid",
        borderColor: isActive
          ? theme.zoneGlow(0.6)
          : theme.zoneGlow(0.2),
        transition: "opacity 0.5s ease-out, transform 0.5s ease-out, border-color 0.5s ease-out",
      }}
    >
      <div className="flex items-center justify-between mb-1">
        <span
          className="text-xs font-mono"
          style={{ color: theme.textMuted }}
        >
          {zone.number}
        </span>
        <span
          className="inline-block rounded-full"
          style={{
            width: 6,
            height: 6,
            backgroundColor: isActive
              ? theme.zoneGlow(1)
              : theme.dotInactive,
            boxShadow: isActive
              ? `0 0 6px ${theme.zoneGlow(0.8)}`
              : "none",
            transition: "background-color 0.5s ease-out, box-shadow 0.5s ease-out",
          }}
        />
      </div>
      <div
        className="text-sm font-medium mb-0.5"
        style={{ color: theme.text }}
      >
        {zone.name}
      </div>
      <div
        className="text-xs leading-snug"
        style={{ color: theme.textMuted }}
      >
        {zone.description}
      </div>
    </div>
  );
}
