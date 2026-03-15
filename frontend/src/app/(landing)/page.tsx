"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import HeroSection from "@/components/landing/hero-section";
import HowItWorksSection from "@/components/landing/how-it-works-section";
import StorySection from "@/components/landing/story-section";
import CtaSection from "@/components/landing/cta-section";
import Footer from "@/components/landing/footer";

export default function LandingPage() {
  const isDark = usePrefersDark();

  return (
    <div className="relative">
      <HeroSection isDark={isDark} />
      <HowItWorksSection isDark={isDark} />
      <StorySection isDark={isDark} />
      <CtaSection isDark={isDark} />
      <Footer isDark={isDark} />
    </div>
  );
}
