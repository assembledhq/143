import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardAction,
  CardContent,
  CardFooter,
} from "./card";

describe("Card", () => {
  it("renders children", () => {
    render(<Card>Card content</Card>);
    expect(screen.getByText("Card content")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<Card>content</Card>);
    expect(screen.getByText("content").closest("[data-slot='card']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<Card className="my-custom-class">content</Card>);
    const el = screen.getByText("content");
    expect(el).toHaveClass("my-custom-class");
  });

  it("passes through additional props", () => {
    render(<Card data-testid="test-card">content</Card>);
    expect(screen.getByTestId("test-card")).toBeInTheDocument();
  });

  it("uses the raised surface token by default", () => {
    render(<Card data-testid="surface-card">content</Card>);
    expect(screen.getByTestId("surface-card")).toHaveClass("bg-surface-raised");
  });
});

describe("CardHeader", () => {
  it("renders children", () => {
    render(<CardHeader>Header text</CardHeader>);
    expect(screen.getByText("Header text")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<CardHeader>header</CardHeader>);
    expect(screen.getByText("header").closest("[data-slot='card-header']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<CardHeader className="header-class">header</CardHeader>);
    expect(screen.getByText("header")).toHaveClass("header-class");
  });
});

describe("CardTitle", () => {
  it("renders children", () => {
    render(<CardTitle>My Title</CardTitle>);
    expect(screen.getByText("My Title")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<CardTitle>title</CardTitle>);
    expect(screen.getByText("title").closest("[data-slot='card-title']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<CardTitle className="title-class">title</CardTitle>);
    expect(screen.getByText("title")).toHaveClass("title-class");
  });
});

describe("CardDescription", () => {
  it("renders children", () => {
    render(<CardDescription>Description text</CardDescription>);
    expect(screen.getByText("Description text")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<CardDescription>desc</CardDescription>);
    expect(screen.getByText("desc").closest("[data-slot='card-description']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<CardDescription className="desc-class">desc</CardDescription>);
    expect(screen.getByText("desc")).toHaveClass("desc-class");
  });
});

describe("CardAction", () => {
  it("renders children", () => {
    render(<CardAction>Action button</CardAction>);
    expect(screen.getByText("Action button")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<CardAction>action</CardAction>);
    expect(screen.getByText("action").closest("[data-slot='card-action']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<CardAction className="action-class">action</CardAction>);
    expect(screen.getByText("action")).toHaveClass("action-class");
  });
});

describe("CardContent", () => {
  it("renders children", () => {
    render(<CardContent>Body content</CardContent>);
    expect(screen.getByText("Body content")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<CardContent>body</CardContent>);
    expect(screen.getByText("body").closest("[data-slot='card-content']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<CardContent className="content-class">body</CardContent>);
    expect(screen.getByText("body")).toHaveClass("content-class");
  });
});

describe("CardFooter", () => {
  it("renders children", () => {
    render(<CardFooter>Footer content</CardFooter>);
    expect(screen.getByText("Footer content")).toBeInTheDocument();
  });

  it("has data-slot attribute", () => {
    render(<CardFooter>footer</CardFooter>);
    expect(screen.getByText("footer").closest("[data-slot='card-footer']")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    render(<CardFooter className="footer-class">footer</CardFooter>);
    expect(screen.getByText("footer")).toHaveClass("footer-class");
  });
});

describe("Card composition", () => {
  it("renders a fully composed card", () => {
    render(
      <Card data-testid="composed-card">
        <CardHeader>
          <CardTitle>Composed Title</CardTitle>
          <CardDescription>Composed description</CardDescription>
          <CardAction>
            <button>Edit</button>
          </CardAction>
        </CardHeader>
        <CardContent>Composed body</CardContent>
        <CardFooter>Composed footer</CardFooter>
      </Card>
    );

    expect(screen.getByTestId("composed-card")).toBeInTheDocument();
    expect(screen.getByText("Composed Title")).toBeInTheDocument();
    expect(screen.getByText("Composed description")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByText("Composed body")).toBeInTheDocument();
    expect(screen.getByText("Composed footer")).toBeInTheDocument();
  });
});
