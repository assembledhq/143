import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { PriorityWeights, weightsTotal, areWeightsValid } from "./priority-weights";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

function validWeights() {
  return {
    customerImpact: "0.40",
    severity: "0.30",
    recency: "0.20",
    revenueRisk: "0.10",
  };
}

function invalidWeights() {
  return {
    customerImpact: "0.50",
    severity: "0.30",
    recency: "0.20",
    revenueRisk: "0.20",
  };
}

describe("PriorityWeights", () => {
  it("renders all 4 weight labels", () => {
    renderWithProviders(
      <PriorityWeights weights={validWeights()} onChange={vi.fn()} />,
    );

    expect(screen.getByText("Customer impact")).toBeInTheDocument();
    expect(screen.getByText("Severity")).toBeInTheDocument();
    expect(screen.getByText("Recency")).toBeInTheDocument();
    expect(screen.getByText("Revenue risk")).toBeInTheDocument();
  });

  it("shows current weight values", () => {
    renderWithProviders(
      <PriorityWeights weights={validWeights()} onChange={vi.fn()} />,
    );

    expect(screen.getByText("0.40")).toBeInTheDocument();
    expect(screen.getByText("0.30")).toBeInTheDocument();
    expect(screen.getByText("0.20")).toBeInTheDocument();
    expect(screen.getByText("0.10")).toBeInTheDocument();
  });

  it("shows sum display", () => {
    renderWithProviders(
      <PriorityWeights weights={validWeights()} onChange={vi.fn()} />,
    );

    expect(screen.getByText(/Sum:/)).toBeInTheDocument();
    expect(screen.getByText(/1\.00 \/ 1\.00/)).toBeInTheDocument();
  });

  it("shows error message when weights do not sum to 1.0", () => {
    renderWithProviders(
      <PriorityWeights weights={invalidWeights()} onChange={vi.fn()} />,
    );

    expect(screen.getByText("Weights must sum to 1.0")).toBeInTheDocument();
  });

  it("does not show error message when weights sum to 1.0", () => {
    renderWithProviders(
      <PriorityWeights weights={validWeights()} onChange={vi.fn()} />,
    );

    expect(screen.queryByText("Weights must sum to 1.0")).not.toBeInTheDocument();
  });
});

describe("weightsTotal", () => {
  it("returns the correct sum of all weight values", () => {
    const total = weightsTotal(validWeights());
    expect(total).toBeCloseTo(1.0);
  });

  it("returns the correct sum for non-1.0 weights", () => {
    const total = weightsTotal(invalidWeights());
    expect(total).toBeCloseTo(1.2);
  });

  it("handles empty string values as 0", () => {
    const total = weightsTotal({
      customerImpact: "",
      severity: "",
      recency: "",
      revenueRisk: "",
    });
    expect(total).toBeCloseTo(0);
  });
});

describe("areWeightsValid", () => {
  it("returns true when weights sum to exactly 1.0", () => {
    expect(areWeightsValid(validWeights())).toBe(true);
  });

  it("returns false when weights do not sum to 1.0", () => {
    expect(areWeightsValid(invalidWeights())).toBe(false);
  });

  it("returns true within tolerance (0.995 should be valid)", () => {
    const nearlyValid = {
      customerImpact: "0.40",
      severity: "0.30",
      recency: "0.20",
      revenueRisk: "0.095",
    };
    // Sum = 0.995, which is within 0.01 strict tolerance (|0.995 - 1.0| = 0.005 < 0.01)
    expect(areWeightsValid(nearlyValid)).toBe(true);
  });

  it("returns true within tolerance (1.005 should be valid)", () => {
    const slightlyOver = {
      customerImpact: "0.40",
      severity: "0.30",
      recency: "0.20",
      revenueRisk: "0.105",
    };
    // Sum = 1.005, which is within 0.01 tolerance
    expect(areWeightsValid(slightlyOver)).toBe(true);
  });

  it("returns false when sum is far from 1.0", () => {
    const farOff = {
      customerImpact: "0.10",
      severity: "0.10",
      recency: "0.10",
      revenueRisk: "0.10",
    };
    // Sum = 0.40
    expect(areWeightsValid(farOff)).toBe(false);
  });
});
