"use client";

import { usePrefersDark } from "@/hooks/use-prefers-dark";
import HeroSection from "@/components/landing/hero-section";
import HowItWorksSection from "@/components/landing/how-it-works-section";
import IntegrationsSection from "@/components/landing/integrations-section";
import CtaSection from "@/components/landing/cta-section";
import Footer from "@/components/landing/footer";
import { landingLayout as layout } from "@/components/landing/landing-layout";

export default function LandingPage() {
  const isDark = usePrefersDark();

  return (
    <div className={layout.pageRoot}>
      <HeroSection isDark={isDark} />
      <HowItWorksSection isDark={isDark} />
      <IntegrationsSection isDark={isDark} />
      <CtaSection isDark={isDark} />
      <Footer isDark={isDark} />
    </div>
  );
}
