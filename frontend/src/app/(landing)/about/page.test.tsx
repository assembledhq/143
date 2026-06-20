import { describe, expect, it } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import AboutPage from "./page";

describe("AboutPage", () => {
  it("renders as a plain editorial note from John", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("heading", { name: "Why we built 143" })).toBeInTheDocument();
    expect(screen.getByRole("article", { name: "Why we built 143" })).toBeInTheDocument();
    expect(screen.queryByText("Founder note")).not.toBeInTheDocument();
    expect(screen.getByText(/I really hope you like it/i)).toBeInTheDocument();
    expect(screen.getByText("John")).toBeInTheDocument();
  });

  it("explains why 143 was built for production teams", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByText(/very impressive in fresh repos/i)).toBeInTheDocument();
    expect(screen.getByText(/we needed more in order to actually get them working well/i)).toBeInTheDocument();
    expect(screen.getByText(/across teams of engineers and non-engineers/i)).toBeInTheDocument();
    expect(screen.getByText(/That.s why we built 143 and open sourced it/i)).toBeInTheDocument();
    expect(screen.getByText(/real product work, not just demos and internal tools/i)).toBeInTheDocument();
    expect(screen.getByText(/production systems across teams/i)).toBeInTheDocument();
    expect(screen.getByText(/shared infrastructure problem/i)).toBeInTheDocument();
    expect(screen.getByText(/vibe coding is not the right word/i)).toBeInTheDocument();
    expect(screen.queryByText(/We had real FOMO/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Also, vibe coding/i)).not.toBeInTheDocument();
  });

  it("describes how 143 helps non-engineers write code safely", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getAllByText(/domain experts/i).length).toBeGreaterThan(0);
    expect(screen.getByText(/write real code/i)).toBeInTheDocument();
    expect(screen.getAllByText(/review gates/i).length).toBeGreaterThan(0);
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
    expect(screen.getByRole("note", { name: "For non-engineers" })).toBeInTheDocument();
    expect(screen.getByRole("note", { name: "Why open source" })).toBeInTheDocument();
  });

  it("states the open-source and pricing principles", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.getByRole("heading", { name: /open source/i })).toBeInTheDocument();
    expect(screen.getByText(/Ruby on Rails/i)).toBeInTheDocument();
    expect(screen.getByText(/Aaron Patterson/i)).toBeInTheDocument();
    expect(screen.queryByText(/tenderlove/i)).not.toBeInTheDocument();
    expect(screen.getByText(/charging just for the containers you run/i)).toBeInTheDocument();
  });

  it("avoids heavy product-page visual sections", () => {
    renderWithProviders(<AboutPage />);

    expect(screen.queryByLabelText("143 origin visual")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "From individual tools to shared infrastructure" })).not.toBeInTheDocument();
  });
});
