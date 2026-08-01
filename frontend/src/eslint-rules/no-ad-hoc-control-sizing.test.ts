import { Linter } from "eslint";
import { describe, expect, it } from "vitest";

import { noAdHocControlSizing } from "../../eslint-rules/no-ad-hoc-control-sizing.mjs";

const linter = new Linter();
const config = [
  {
    plugins: {
      custom: {
        rules: {
          "no-ad-hoc-control-sizing": noAdHocControlSizing,
        },
      },
    },
    languageOptions: {
      ecmaVersion: "latest" as const,
      sourceType: "module" as const,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    rules: {
      "custom/no-ad-hoc-control-sizing": "error" as const,
    },
  },
];

function lint(code: string) {
  return linter.verify(code, config);
}

describe("no-ad-hoc-control-sizing", () => {
  it.each([
    '<Input className="h-8" />',
    '<SelectTrigger className={cn("sm:h-7", className)} />',
    '<CommandInput className="py-0" />',
    '<ControlTrigger className="[&]:h-8" />',
    '<Command className="**:data-[slot=command-input-wrapper]:h-12" />',
    '<BranchPicker buttonClassName="min-h-10 w-full" />',
    '<AutomationModelSelect triggerClassName="h-[42px]" />',
    '<Input style={{ height: 32 }} />',
  ])("rejects ad-hoc sizing in %s", (jsx) => {
    const messages = lint(`const control = ${jsx};`);

    expect(messages).toHaveLength(1);
    expect(messages[0]?.ruleId).toBe("custom/no-ad-hoc-control-sizing");
  });

  it.each([
    '<Input density="compact" className="w-full" />',
    '<SelectTrigger density="dense" className="px-1.5" />',
    '<CommandInput density="default" />',
    '<BranchPicker density="compact" buttonClassName="w-full" />',
    '<Button className="h-8" />',
  ])("allows shared density sizing in %s", (jsx) => {
    expect(lint(`const control = ${jsx};`)).toEqual([]);
  });

  it("follows aliased imports and locally defined class constants", () => {
    const messages = lint(`
      import { Input as TextInput } from "@/components/ui/input";
      const controlClass = cn("w-full", "!h-8");
      const control = <TextInput className={controlClass} />;
    `);

    expect(messages).toHaveLength(1);
    expect(messages[0]?.ruleId).toBe("custom/no-ad-hoc-control-sizing");
  });

  it("allows aliased controls that use density and appearance-only classes", () => {
    expect(
      lint(`
        import { ControlTrigger as PickerTrigger } from "@/components/ui/control-trigger";
        const controlClass = cn("w-full", isOpen && "bg-accent");
        const control = <PickerTrigger density="compact" className={controlClass} />;
      `),
    ).toEqual([]);
  });
});
