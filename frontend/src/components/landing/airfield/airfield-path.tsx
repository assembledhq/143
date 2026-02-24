"use client";

import React from "react";
import { ZONES, getActiveZone } from "./zones";
import { AIRFIELD_DARK, AIRFIELD_LIGHT } from "./airfield-theme";

interface AirfieldPathProps {
  progress: number;
  isDark: boolean;
}

export default function AirfieldPath({ progress, isDark }: AirfieldPathProps) {
  const theme = isDark ? AIRFIELD_DARK : AIRFIELD_LIGHT;
  const activeZone = getActiveZone(progress);

  return (
    <div
      style={{
        position: "absolute",
        bottom: "2.5rem",
        left: "50%",
        transform: "translateX(-50%)",
        display: "flex",
        alignItems: "center",
        pointerEvents: "none",
        zIndex: 10,
      }}
    >
      {ZONES.map((zone, i) => {
        const isReached = progress >= zone.progressStart;
        const isCurrent = i === activeZone;

        return (
          <React.Fragment key={zone.id}>
            {i > 0 && (
              <div
                style={{
                  width: 28,
                  height: 2,
                  backgroundColor: isReached
                    ? theme.dotActive
                    : theme.dotInactive,
                  transition: "background-color 0.5s",
                }}
              />
            )}
            <div
              style={{
                width: isCurrent ? 10 : 7,
                height: isCurrent ? 10 : 7,
                borderRadius: "50%",
                backgroundColor: isReached
                  ? theme.dotActive
                  : theme.dotInactive,
                boxShadow: isCurrent
                  ? `0 0 8px ${theme.dotActive}, 0 0 16px ${theme.dotActive}`
                  : "none",
                transition: "all 0.4s ease-out",
              }}
            />
          </React.Fragment>
        );
      })}
    </div>
  );
}
