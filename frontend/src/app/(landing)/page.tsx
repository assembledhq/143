"use client";

import { useEffect, useState } from "react";
import HeroSection from "@/components/landing/hero-section";
import AirfieldSection from "@/components/landing/airfield/airfield-section";
import CtaSection from "@/components/landing/cta-section";

export default function LandingPage() {
  const [isDark, setIsDark] = useState(true);

  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => setIsDark(mq.matches);
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);

  return (
    <div className="relative">
      <HeroSection isDark={isDark} />
      <AirfieldSection isDark={isDark} />
      <CtaSection isDark={isDark} />
    </div>
  );
}
