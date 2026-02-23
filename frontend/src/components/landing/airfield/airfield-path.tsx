"use client";

import { useEffect, useRef, useState } from "react";
import { ZONES } from "./zones";
import { AIRFIELD_DARK, AIRFIELD_LIGHT } from "./airfield-theme";

interface AirfieldPathProps {
  progress: number;
  isDark: boolean;
}

function buildPath(): string {
  const points = ZONES.map((z) => ({
    x: z.position.x * 1000,
    y: z.position.y * 1000,
  }));

  if (points.length === 0) return "";

  let d = `M ${points[0].x} ${points[0].y}`;

  for (let i = 1; i < points.length; i++) {
    const prev = points[i - 1];
    const curr = points[i];
    const cx = (prev.x + curr.x) / 2;
    const cy = (prev.y + curr.y) / 2;
    d += ` Q ${prev.x + (cx - prev.x) * 0.5} ${prev.y + (cy - prev.y) * 1.4}, ${cx} ${cy}`;
    d += ` Q ${cx + (curr.x - cx) * 0.5} ${cy + (curr.y - cy) * 0.6}, ${curr.x} ${curr.y}`;
  }

  return d;
}

const FILTER_ID = "airfield-glow";

export default function AirfieldPath({ progress, isDark }: AirfieldPathProps) {
  const pathRef = useRef<SVGPathElement>(null);
  const [totalLength, setTotalLength] = useState(0);

  useEffect(() => {
    if (pathRef.current) {
      setTotalLength(pathRef.current.getTotalLength());
    }
  }, []);

  const theme = isDark ? AIRFIELD_DARK : AIRFIELD_LIGHT;
  const d = buildPath();
  const dashOffset = totalLength * (1 - progress);

  return (
    <svg
      style={{ position: "absolute", inset: 0, width: "100%", height: "100%", pointerEvents: "none" }}
      viewBox="0 0 1000 1000"
      preserveAspectRatio="none"
    >
      <defs>
        <filter id={FILTER_ID} x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur in="SourceGraphic" stdDeviation="3.5" />
        </filter>
      </defs>

      {/* Glow path behind the main path */}
      {totalLength > 0 && (
        <path
          d={d}
          fill="none"
          stroke={theme.pathStroke}
          strokeWidth={6}
          strokeLinecap="round"
          strokeDasharray={totalLength}
          strokeDashoffset={dashOffset}
          filter={`url(#${FILTER_ID})`}
          opacity={0.4}
          style={{ transition: "stroke-dashoffset 0.1s ease-out" }}
        />
      )}

      {/* Main path */}
      <path
        ref={pathRef}
        d={d}
        fill="none"
        stroke={theme.pathStroke}
        strokeWidth={3}
        strokeLinecap="round"
        strokeDasharray={totalLength || undefined}
        strokeDashoffset={totalLength > 0 ? dashOffset : undefined}
        style={{ transition: "stroke-dashoffset 0.1s ease-out" }}
      />

      {/* Zone dot markers */}
      {ZONES.map((zone) => {
        const cx = zone.position.x * 1000;
        const cy = zone.position.y * 1000;
        const isActive = zone.progressStart <= progress;

        return (
          <g key={zone.id}>
            {/* Glow behind active dots */}
            {isActive && (
              <circle
                cx={cx}
                cy={cy}
                r={12}
                fill={theme.dotActive}
                filter={`url(#${FILTER_ID})`}
                opacity={0.5}
              />
            )}
            <circle
              cx={cx}
              cy={cy}
              r={8}
              fill={isActive ? theme.dotActive : theme.dotInactive}
            />
          </g>
        );
      })}
    </svg>
  );
}
