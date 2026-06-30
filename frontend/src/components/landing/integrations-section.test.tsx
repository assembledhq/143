import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import IntegrationsSection from "./integrations-section";
import { landingTypography } from "./landing-typography";

describe("IntegrationsSection", () => {
  it("uses the feature heading scale for the integrations headline", () => {
    render(<IntegrationsSection isDark={false} />);

    const heading = screen.getByRole("heading", {
      level: 2,
      name: "Connect your engineering tools.",
    });

    expect(heading.className).toContain(landingTypography.featureTitle);
    expect(heading.className).not.toContain(landingTypography.sectionTitle);
  });

  it("shows agent choice and cost controls as supporting features", () => {
    render(<IntegrationsSection isDark={false} />);

    expect(
      screen.getByRole("heading", {
        level: 3,
        name: "Use the best agent for the job",
      }),
    ).toBeInTheDocument();
    expect(screen.getByText(/open-source models/i)).toBeInTheDocument();
    expect(screen.getByText(/bundled coding-agent subscriptions/i)).toBeInTheDocument();
  });
});
