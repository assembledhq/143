import { createContext, memo, useContext } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Components } from "react-markdown";
import { cn } from "@/lib/utils";

const remarkPlugins = [remarkGfm];

// Context to let `Code` know it's inside a `pre` (block code).
const BlockCodeContext = createContext(false);

// Extracted as a named component so useContext satisfies rules-of-hooks.
function Code({ className, children, ...props }: React.ComponentProps<"code">) {
  const isBlock = useContext(BlockCodeContext);

  if (isBlock) {
    return (
      <code className={cn("font-mono text-xs", className)} {...props}>
        {children}
      </code>
    );
  }

  return (
    <code
      className={cn(
        "box-decoration-clone break-all whitespace-normal rounded border border-border bg-background px-1 py-0.5 font-mono text-xs leading-relaxed",
        className
      )}
      {...props}
    >
      {children}
    </code>
  );
}

const components: Components = {
  // Code blocks: `pre` always wraps block-level code from fenced blocks
  pre({ children }) {
    return (
      <BlockCodeContext.Provider value={true}>
        <pre className="my-2 rounded-md border border-border bg-background p-3 overflow-x-auto text-xs">
          {children}
        </pre>
      </BlockCodeContext.Provider>
    );
  },
  code: Code,
  // Paragraphs
  p({ children }) {
    return <p className="mb-2 last:mb-0 leading-relaxed break-words">{children}</p>;
  },
  // Headings
  h1({ children }) {
    return <h1 className="mb-2 mt-3 first:mt-0 text-base font-semibold">{children}</h1>;
  },
  h2({ children }) {
    return <h2 className="mb-2 mt-3 first:mt-0 text-sm font-semibold">{children}</h2>;
  },
  h3({ children }) {
    return <h3 className="mb-1 mt-2 first:mt-0 text-sm font-medium">{children}</h3>;
  },
  // Lists
  ul({ children }) {
    return <ul className="mb-2 ml-4 list-disc space-y-0.5 last:mb-0">{children}</ul>;
  },
  ol({ children, className, ...props }) {
    return (
      <ol
        className={cn("mb-2 ml-4 list-decimal space-y-0.5 last:mb-0", className)}
        {...props}
      >
        {children}
      </ol>
    );
  },
  li({ children }) {
    return <li className="leading-relaxed">{children}</li>;
  },
  // Links
  a({ href, children }) {
    return (
      <a
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        className="text-primary underline underline-offset-2 hover:text-primary/80"
      >
        {children}
      </a>
    );
  },
  // Blockquotes
  blockquote({ children }) {
    return (
      <blockquote className="my-2 border-l-2 border-primary/30 pl-3 text-muted-foreground italic">
        {children}
      </blockquote>
    );
  },
  // Tables (GFM)
  table({ children }) {
    return (
      <div className="my-2 overflow-x-auto">
        <table className="w-full text-xs border-collapse">{children}</table>
      </div>
    );
  },
  th({ children }) {
    return (
      <th className="border border-border bg-muted/50 px-2 py-1 text-left font-medium">
        {children}
      </th>
    );
  },
  td({ children }) {
    return <td className="border border-border px-2 py-1">{children}</td>;
  },
  // Horizontal rule
  hr() {
    return <hr className="my-3 border-border" />;
  },
  // Strong / emphasis
  strong({ children }) {
    return <strong className="font-semibold">{children}</strong>;
  },
};

interface MarkdownContentProps {
  content: string;
  className?: string;
}

export const MarkdownContent = memo(function MarkdownContent({
  content,
  className,
}: MarkdownContentProps) {
  const markdown = (
    <ReactMarkdown remarkPlugins={remarkPlugins} components={components}>
      {content}
    </ReactMarkdown>
  );

  if (className) {
    return <div className={className}>{markdown}</div>;
  }

  return markdown;
});
