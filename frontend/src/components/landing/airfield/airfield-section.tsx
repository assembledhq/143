"use client";

import { useRef } from "react";
import { useScrollProgress } from "@/hooks/use-scroll-progress";
import AirfieldCanvas from "./airfield-canvas";

interface AirfieldSectionProps {
  isDark: boolean;
}

export default function AirfieldSection({ isDark }: AirfieldSectionProps) {
  const spacerRef = useRef<HTMLDivElement>(null);
  const progress = useScrollProgress(spacerRef);

  return (
    <div ref={spacerRef} className="relative h-[700vh]">
      <div
        className="sticky top-0 h-screen w-full overflow-hidden"
        style={{ willChange: "transform" }}
      >
        <AirfieldCanvas progress={progress} isDark={isDark} />
      </div>
    </div>
  );
}
