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

  it("keeps the section focused on team tool integrations", () => {
    render(<IntegrationsSection isDark={false} />);

    expect(screen.getByText("05 Integrations")).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Run any coding agent." }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("GitHub")).toBeInTheDocument();
  });
});
