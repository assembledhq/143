import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

type Imports = {
  nextLinkNames: Set<string>;
  buttonNames: Set<string>;
};

type Finding = {
  file: string;
  line: number;
  type: "link-contains-button" | "button-contains-interactive";
  outer: string;
  inner: string;
};

const SRC_ROOT = path.dirname(fileURLToPath(import.meta.url));
const FRONTEND_ROOT = path.resolve(SRC_ROOT, "..");

describe("interactive nesting", () => {
  it("does not nest buttons inside links or links inside buttons", () => {
    const files = collectSourceFiles(SRC_ROOT);
    const findings = files.flatMap((filePath) => findInteractiveNesting(filePath));

    expect(findings).toEqual([]);
  });

  it("treats dynamic asChild expressions as unsafe", () => {
    const fixturePath = path.join(SRC_ROOT, "__interactive-nesting-dynamic-aschild__.test.tsx");
    const source = `
      import Link from "next/link";
      import { Button } from "@/components/ui/button";

      export function Example({ asChild }: { asChild: boolean }) {
        return (
          <Button asChild={asChild}>
            <Link href="/settings">Settings</Link>
          </Button>
        );
      }
    `;

    fs.writeFileSync(fixturePath, source);

    try {
      expect(findInteractiveNesting(fixturePath)).toMatchObject([
        {
          file: path.relative(FRONTEND_ROOT, fixturePath),
          type: "button-contains-interactive",
          outer: "Button",
          inner: "Link",
        },
      ]);
    } finally {
      fs.rmSync(fixturePath, { force: true });
    }
  });
});

function collectSourceFiles(root: string): string[] {
  const files: string[] = [];

  function walk(dir: string) {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const fullPath = path.join(dir, entry.name);

      if (entry.isDirectory()) {
        walk(fullPath);
        continue;
      }

      if (!entry.isFile()) continue;
      if (!/\.(tsx|jsx)$/.test(entry.name)) continue;
      if (/\.test\.(tsx|jsx)$/.test(entry.name)) continue;

      files.push(fullPath);
    }
  }

  walk(root);
  return files;
}

function findInteractiveNesting(filePath: string): Finding[] {
  const sourceText = fs.readFileSync(filePath, "utf8");
  const sourceFile = ts.createSourceFile(filePath, sourceText, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const imports = collectImports(sourceFile);
  const findings: Finding[] = [];

  function visit(node: ts.Node) {
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const outer = describeElement(node, imports);
      const opening = ts.isJsxElement(node) ? node.openingElement : node;
      const line = sourceFile.getLineAndCharacterOfPosition(opening.getStart()).line + 1;
      const descendants = collectDescendantJSX(node);

      if (outer.isAnchor || outer.isLink) {
        const nestedButton = descendants.find((descendant) => {
          const inner = describeElement(descendant, imports);
          return inner.isRawButton || inner.isButton;
        });

        if (nestedButton) {
          findings.push({
            file: path.relative(FRONTEND_ROOT, filePath),
            line,
            type: "link-contains-button",
            outer: outer.tag,
            inner: describeElement(nestedButton, imports).tag,
          });
        }
      }

      if (outer.isRawButton || (outer.isButton && !outer.asChild)) {
        const nestedInteractive = descendants.find((descendant) => {
          const inner = describeElement(descendant, imports);
          return inner.isAnchor || inner.isLink || inner.isRawButton || (inner.isButton && !inner.asChild);
        });

        if (nestedInteractive) {
          findings.push({
            file: path.relative(FRONTEND_ROOT, filePath),
            line,
            type: "button-contains-interactive",
            outer: outer.tag,
            inner: describeElement(nestedInteractive, imports).tag,
          });
        }
      }
    }

    ts.forEachChild(node, visit);
  }

  visit(sourceFile);

  return findings;
}

function collectImports(sourceFile: ts.SourceFile): Imports {
  const nextLinkNames = new Set<string>();
  const buttonNames = new Set<string>();

  sourceFile.forEachChild((node) => {
    if (!ts.isImportDeclaration(node) || !ts.isStringLiteral(node.moduleSpecifier)) return;

    const modulePath = node.moduleSpecifier.text;
    const clause = node.importClause;
    if (!clause) return;

    if (modulePath === "next/link") {
      if (clause.name) nextLinkNames.add(clause.name.text);
      if (clause.namedBindings && ts.isNamedImports(clause.namedBindings)) {
        for (const element of clause.namedBindings.elements) {
          nextLinkNames.add(element.name.text);
        }
      }
    }

    if ((modulePath === "@/components/ui/button" || modulePath.endsWith("/components/ui/button")) &&
      clause.namedBindings &&
      ts.isNamedImports(clause.namedBindings)) {
      for (const element of clause.namedBindings.elements) {
        const importedName = (element.propertyName || element.name).text;
        if (importedName === "Button") {
          buttonNames.add(element.name.text);
        }
      }
    }
  });

  return { nextLinkNames, buttonNames };
}

function collectDescendantJSX(node: ts.JsxElement | ts.JsxSelfClosingElement): Array<ts.JsxElement | ts.JsxSelfClosingElement> {
  const descendants: Array<ts.JsxElement | ts.JsxSelfClosingElement> = [];

  function visit(child: ts.Node) {
    if ((ts.isJsxElement(child) || ts.isJsxSelfClosingElement(child)) && child !== node) {
      descendants.push(child);
    }
    ts.forEachChild(child, visit);
  }

  ts.forEachChild(node, visit);

  return descendants;
}

function describeElement(node: ts.JsxElement | ts.JsxSelfClosingElement, imports: Imports) {
  const opening = ts.isJsxElement(node) ? node.openingElement : node;
  const tag = getTagName(opening.tagName);
  const isAnchor = tag === "a";
  const isLink = imports.nextLinkNames.has(tag);
  const isRawButton = tag === "button";
  const isButton = imports.buttonNames.has(tag);
  const asChild = isButton && hasStaticallyEnabledAsChild(opening.attributes);

  return { tag, isAnchor, isLink, isRawButton, isButton, asChild };
}

function hasStaticallyEnabledAsChild(attributes: ts.JsxAttributes): boolean {
  for (const prop of attributes.properties) {
    if (!ts.isJsxAttribute(prop) || getAttributeName(prop.name) !== "asChild") continue;

    if (!prop.initializer) return true;
    if (ts.isStringLiteral(prop.initializer)) return true;
    if (ts.isJsxExpression(prop.initializer)) {
      const expression = prop.initializer.expression;
      if (!expression) return true;
      return expression.kind === ts.SyntaxKind.TrueKeyword;
    }

    return false;
  }

  return false;
}

function getTagName(tagName: ts.JsxTagNameExpression): string {
  if (ts.isIdentifier(tagName)) return tagName.text;
  if (ts.isJsxNamespacedName(tagName)) return `${tagName.namespace.text}:${tagName.name.text}`;
  if (ts.isPropertyAccessExpression(tagName)) return tagName.name.text;
  return tagName.getText();
}

function getAttributeName(attributeName: ts.JsxAttributeName): string {
  if (ts.isIdentifier(attributeName)) return attributeName.text;
  return `${attributeName.namespace.text}:${attributeName.name.text}`;
}
