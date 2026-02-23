"use client";

import { useRef } from "react";
import { useScrollProgress } from "@/hooks/use-scroll-progress";
import { ZONES, getActiveZone, getZoneProgress } from "./zones";
import AirfieldCanvas from "./airfield-canvas";
import AirfieldPath from "./airfield-path";
import ZoneCard from "./zone-card";

interface AirfieldSectionProps {
  isDark: boolean;
}

export default function AirfieldSection({ isDark }: AirfieldSectionProps) {
  const spacerRef = useRef<HTMLDivElement>(null);
  const progress = useScrollProgress(spacerRef);
  const activeZone = getActiveZone(progress);

  return (
    <div ref={spacerRef} className="relative h-[700vh]">
      <div
        className="sticky top-0 h-screen w-full overflow-hidden"
        style={{ willChange: "transform" }}
      >
        <AirfieldCanvas progress={progress} isDark={isDark} />
        <AirfieldPath progress={progress} isDark={isDark} />
        {ZONES.map((zone, i) => (
          <ZoneCard
            key={zone.id}
            zone={zone}
            isActive={i === activeZone}
            zoneProgress={getZoneProgress(progress, i)}
            isDark={isDark}
            index={i}
          />
        ))}
      </div>
    </div>
  );
}
