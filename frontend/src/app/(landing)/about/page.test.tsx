import { describe, expect, it } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import AboutPage from "./page";

describe("AboutPage", () => {
  it("renders as a plain editorial note from John", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("heading", { name: "Why we built 143.dev" })).toBeInTheDocument();
    expect(screen.getByRole("article", { name: "Why we built 143" })).toBeInTheDocument();
    expect(screen.queryByText("Founder note")).not.toBeInTheDocument();
    expect(screen.getByText(/I really hope you like it/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "John Wang" })).toBeInTheDocument();
  });

  it("explains why 143 was built for production teams", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByText(/non-engineers can fix things too/i)).toBeInTheDocument();
    expect(screen.getByText(/built for engineers by engineers/i)).toBeInTheDocument();
    expect(screen.getByText(/real product work, not just demos and internal tools/i)).toBeInTheDocument();
    expect(screen.getByText(/shared across the team/i)).toBeInTheDocument();
    expect(screen.getByText(/Stripe Minions and Ramp Inspect/i)).toBeInTheDocument();
    expect(screen.getByText(/That's why we built 143\./i)).toBeInTheDocument();
    expect(screen.queryByText(/We had real FOMO/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Also, vibe coding/i)).not.toBeInTheDocument();
  });

  it("organizes the editorial note into readable sections", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("heading", { name: "Where it started" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "What we built" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Open source for everyone" })).toBeInTheDocument();
  });

  it("describes team-visible agent infrastructure", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByText(/auto-approve low-risk changes against thresholds you define/i)).toBeInTheDocument();
    expect(screen.getByText(/same repos, credentials, tools, logs,\s*docs/i)).toBeInTheDocument();
    expect(screen.getByText(/product context available to the whole team/i)).toBeInTheDocument();
  });

  it("covers the team-level product choices behind 143", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByText(/Automations shouldn't be hidden/i)).toBeInTheDocument();
    expect(screen.getByText(/swap out intelligence/i)).toBeInTheDocument();
    expect(screen.getByText(/set up a great environment once/i)).toBeInTheDocument();
  });

  it("does not render legacy inline callouts", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });

  it("states the open-source principle", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("heading", { name: /open source/i })).toBeInTheDocument();
    expect(screen.getByText(/Ruby on Rails/i)).toBeInTheDocument();
    expect(screen.getByText(/Aaron Patterson/i)).toBeInTheDocument();
    expect(screen.queryByText(/tenderlove/i)).not.toBeInTheDocument();
    expect(screen.getByText(/available in that same spirit/i)).toBeInTheDocument();
  });

  it("avoids heavy product-page visual sections", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.queryByLabelText("143 origin visual")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "From individual tools to shared infrastructure" })).not.toBeInTheDocument();
  });
});
