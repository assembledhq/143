"use client";

import { useEffect, useState } from "react";
import HeroSection from "@/components/landing/hero-section";
import ConnectsSection from "@/components/landing/connects-section";
import HowItWorksSection from "@/components/landing/how-it-works-section";
import AgentsSection from "@/components/landing/agents-section";
import StorySection from "@/components/landing/story-section";
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
      <ConnectsSection isDark={isDark} />
      <HowItWorksSection isDark={isDark} />
      <AgentsSection isDark={isDark} />
      <StorySection isDark={isDark} />
      <CtaSection isDark={isDark} />
    </div>
  );
}
