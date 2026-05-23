import defaultMdxComponents from "fumadocs-ui/mdx";
import { Callout } from "fumadocs-ui/components/callout";
import { Card, Cards } from "fumadocs-ui/components/card";
import { CodeBlock, Pre } from "fumadocs-ui/components/codeblock";
import { Step, Steps } from "fumadocs-ui/components/steps";
import { Tab, Tabs } from "fumadocs-ui/components/tabs";
import Image, { type ImageProps } from "next/image";
import type { MDXComponents } from "mdx/types";
import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
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

export function getDocsMDXComponents(components?: MDXComponents): MDXComponents {
  return {
    ...defaultMdxComponents,
    Callout,
    Card,
    Cards,
    Step,
    Steps,
    Tab,
    Tabs,
    AgentNote,
    ConfigField,
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
