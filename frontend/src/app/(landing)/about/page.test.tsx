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

    expect(screen.getByText(/feel magical in a fresh repo/i)).toBeInTheDocument();
    expect(screen.getByText(/everything around the agent from setup to context/i)).toBeInTheDocument();
    expect(screen.getByText(/real product work, not just demos and internal tools/i)).toBeInTheDocument();
    expect(screen.getByText(/shared infrastructure problem/i)).toBeInTheDocument();
    expect(screen.queryByText(/We had real FOMO/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Also, vibe coding/i)).not.toBeInTheDocument();
  });

  it("organizes the editorial note into readable sections", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("heading", { name: "Where it started" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "What we built" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Open source from day one" })).toBeInTheDocument();
  });

  it("covers the team-level product choices behind 143", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByText(/automations should be visible to the team/i)).toBeInTheDocument();
    expect(screen.getByText(/swap out intelligence/i)).toBeInTheDocument();
    expect(screen.getByText(/set up a great environment once/i)).toBeInTheDocument();
  });

  it("uses inline callouts to annotate the note", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("note", { name: "What was missing" })).toBeInTheDocument();
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
