import defaultMdxComponents from "fumadocs-ui/mdx";
import { Callout } from "fumadocs-ui/components/callout";
import { Card as FumadocsCard, Cards } from "fumadocs-ui/components/card";
import { CodeBlock, Pre } from "fumadocs-ui/components/codeblock";
import { Step, Steps } from "fumadocs-ui/components/steps";
import { Tab, Tabs } from "fumadocs-ui/components/tabs";
import Image, { type ImageProps } from "next/image";
import type { MDXComponents } from "mdx/types";
import type { CSSProperties, ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export function AgentNote({ children }: { children: ReactNode }) {
  return (
    <Callout type="idea" title="For agents">
      {children}
    </Callout>
  );
}

interface ConfigFieldProps {
  name: string;
  type: string;
  required?: boolean;
  defaultValue?: string;
  description: ReactNode;
}

export function ConfigField({
  name,
  type,
  required = false,
  defaultValue,
  description,
}: ConfigFieldProps) {
  return (
    <div className="my-3 rounded-lg border border-border bg-card p-4 shadow-sm">
      <div className="flex flex-wrap items-center gap-2">
        <code className="rounded-md border border-border bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
          {name}
        </code>
        <Badge variant="secondary" className="font-mono text-xs">
          {type}
        </Badge>
        {required ? (
          <Badge variant="outline" className="text-xs">
            required
          </Badge>
        ) : null}
        {defaultValue ? (
          <span className="text-xs text-muted-foreground">default: {defaultValue}</span>
        ) : null}
      </div>
      <div className="mt-2 leading-relaxed text-muted-foreground">{description}</div>
    </div>
  );
}

interface ScreenshotProps extends Omit<ImageProps, "alt"> {
  alt: string;
  caption?: ReactNode;
}

export function Screenshot({ caption, alt, className, ...props }: ScreenshotProps) {
  return (
    <figure className="my-6">
      <Image
        alt={alt}
        className={cn("rounded-lg border border-border shadow-sm", className)}
        {...props}
      />
      {caption ? (
        <figcaption className="mt-2 text-xs leading-relaxed text-muted-foreground">
          {caption}
        </figcaption>
      ) : null}
    </figure>
  );
}

interface FlowDiagramProps {
  caption?: ReactNode;
  items: string[];
}

export function FlowDiagram({ caption, items }: FlowDiagramProps) {
  return (
    <figure className="my-6">
      <Card className="rounded-lg bg-card/70 shadow-sm">
        <CardContent
          className="grid gap-3 p-4 sm:grid-cols-[repeat(var(--flow-columns),minmax(0,1fr))]"
          style={{ "--flow-columns": Math.max(items.length, 1) } as CSSProperties}
        >
          {items.map((item, index) => (
            <div key={`${item}-${index}`} className="flex items-center gap-3">
              <div className="flex min-h-20 flex-1 items-center rounded-lg border border-border bg-background px-3 py-3 text-sm font-medium leading-snug text-foreground">
                {item}
              </div>
              {index < items.length - 1 ? (
                <div className="hidden text-muted-foreground sm:block" aria-hidden="true">
                  -&gt;
                </div>
              ) : null}
            </div>
          ))}
        </CardContent>
      </Card>
      {caption ? (
        <figcaption className="mt-2 text-xs leading-relaxed text-muted-foreground">
          {caption}
        </figcaption>
      ) : null}
    </figure>
  );
}

interface BoundaryDiagramProps {
  caption?: ReactNode;
  leftItems: string[];
  leftTitle: string;
  rightItems: string[];
  rightTitle: string;
}

export function BoundaryDiagram({
  caption,
  leftItems,
  leftTitle,
  rightItems,
  rightTitle,
}: BoundaryDiagramProps) {
  const columns = [
    { title: leftTitle, items: leftItems },
    { title: rightTitle, items: rightItems },
  ];

  return (
    <figure className="my-6">
      <Card className="rounded-lg bg-card/70 shadow-sm">
        <CardContent className="grid gap-4 p-4 md:grid-cols-2">
          {columns.map((column) => (
            <div
              key={column.title}
              className="rounded-lg border border-border bg-background p-4"
            >
              <h3 className="mt-0 text-sm font-semibold text-foreground">{column.title}</h3>
              <ul className="mb-0 mt-3 space-y-2 pl-4 text-sm leading-relaxed text-muted-foreground">
                {column.items.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            </div>
          ))}
        </CardContent>
      </Card>
      {caption ? (
        <figcaption className="mt-2 text-xs leading-relaxed text-muted-foreground">
          {caption}
        </figcaption>
      ) : null}
    </figure>
  );
}

export function getDocsMDXComponents(components?: MDXComponents): MDXComponents {
  return {
    ...defaultMdxComponents,
    Callout,
    Card: FumadocsCard,
    Cards,
    Step,
    Steps,
    Tab,
    Tabs,
    AgentNote,
    BoundaryDiagram,
    ConfigField,
    FlowDiagram,
    Screenshot,
    pre: ({ ref, ...props }) => {
      void ref;
      return (
        <CodeBlock {...props}>
          <Pre>{props.children}</Pre>
        </CodeBlock>
      );
    },
    ...components,
  } satisfies MDXComponents;
}
