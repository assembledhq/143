import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PageTabContent } from "./page-tab-content";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

describe("PageTabContent", () => {
  it("uses the canonical page section rhythm and preserves custom classes", () => {
    render(
      <Tabs defaultValue="reviews">
        <TabsList>
          <TabsTrigger value="reviews">Reviews</TabsTrigger>
        </TabsList>
        <PageTabContent value="reviews" className="max-w-5xl">
          Review content
        </PageTabContent>
      </Tabs>,
    );

    const content = screen.getByText("Review content");

    expect(content).toHaveAttribute("data-slot", "page-tab-content");
    expect(content).toHaveClass("space-y-6", "max-w-5xl");
  });
});
