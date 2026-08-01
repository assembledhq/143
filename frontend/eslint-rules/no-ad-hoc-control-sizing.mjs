const controlledAttributes = new Map([
  ["Input", new Set(["className"])],
  ["SelectTrigger", new Set(["className"])],
  ["CommandInput", new Set(["className"])],
  ["ControlTrigger", new Set(["className"])],
  ["BranchPicker", new Set(["buttonClassName"])],
  ["TimezonePicker", new Set(["className"])],
  ["AutomationModelSelect", new Set(["triggerClassName"])],
]);

const componentImportPatterns = new Map([
  ["Input", /(?:^|\/)(?:ui\/)?input$/],
  ["SelectTrigger", /(?:^|\/)(?:ui\/)?select$/],
  ["CommandInput", /(?:^|\/)(?:ui\/)?command$/],
  ["ControlTrigger", /(?:^|\/)(?:ui\/)?control-trigger$/],
  ["BranchPicker", /(?:^|\/)branch-picker$/],
  ["TimezonePicker", /(?:^|\/)automations\/timezone-picker$/],
  ["AutomationModelSelect", /(?:^|\/)automation-model-select$/],
]);

const adHocSizingPattern =
  /(?:^|[\s"'`])(?:[^\s"'`{}]+:)*!?(?:h|min-h|py)-[^\s"'`}\])]+/i;
const descendantControlSizingPattern =
  /(?:data-\[slot=(?:input|select-trigger|command-input(?:-wrapper)?)\]|\[cmdk-input(?:-wrapper)?\])\]?(?::[a-z0-9-]+)*:(?:h|min-h|py)-/i;
const inlineStyleSizingPattern =
  /(?:^|[{,;\s])["']?(?:height|minHeight|blockSize|minBlockSize|paddingBlock|paddingBlockStart|paddingBlockEnd|paddingTop|paddingBottom)["']?\s*:/;

/** @type {import("eslint").Rule.RuleModule} */
const noAdHocControlSizing = {
  meta: {
    type: "problem",
    docs: {
      description: "Require shared density props instead of ad-hoc control height classes",
    },
    schema: [],
    messages: {
      adHocSizing:
        "Do not size {{component}} through {{attribute}}. Use its density prop and the shared control sizing variants.",
    },
  },
  create(context) {
    const sourceCode = context.sourceCode ?? context.getSourceCode();
    const localControlledAttributes = new Map(controlledAttributes);
    const variableInitializers = new Map();

    function expandedValueText(value) {
      let text = sourceCode.getText(value);
      if (value.type !== "JSXExpressionContainer" || !value.expression) {
        return text;
      }

      const pendingNames = sourceCode
        .getTokens(value.expression)
        .filter((token) => token.type === "Identifier")
        .map((token) => token.value);
      const visitedNames = new Set();

      while (pendingNames.length > 0) {
        const name = pendingNames.pop();
        if (!name || visitedNames.has(name)) continue;
        visitedNames.add(name);

        const initializer = variableInitializers.get(name);
        if (!initializer) continue;
        text += ` ${sourceCode.getText(initializer)}`;
        for (const token of sourceCode.getTokens(initializer)) {
          if (token.type === "Identifier" && !visitedNames.has(token.value)) {
            pendingNames.push(token.value);
          }
        }
      }

      return text;
    }

    return {
      ImportDeclaration(node) {
        const importSource = String(node.source.value);
        for (const specifier of node.specifiers) {
          if (specifier.type !== "ImportSpecifier") continue;
          const importedName =
            specifier.imported.type === "Identifier"
              ? specifier.imported.name
              : String(specifier.imported.value);
          const importPattern = componentImportPatterns.get(importedName);
          if (!importPattern || !importPattern.test(importSource)) continue;

          const attributes = controlledAttributes.get(importedName);
          if (attributes) {
            localControlledAttributes.set(specifier.local.name, attributes);
          }
        }
      },
      VariableDeclarator(node) {
        if (node.id.type === "Identifier" && node.init) {
          variableInitializers.set(node.id.name, node.init);
        }
      },
      JSXOpeningElement(node) {
        const component = sourceCode.getText(node.name);
        const attributes = localControlledAttributes.get(component) ?? new Set();
        const isControlledComponent = attributes.size > 0;

        for (const attribute of node.attributes) {
          if (attribute.type !== "JSXAttribute") continue;
          const attributeName = sourceCode.getText(attribute.name);
          if (!attribute.value) continue;

          const value = expandedValueText(attribute.value);
          const sizesControlledComponent =
            attributes.has(attributeName) && adHocSizingPattern.test(value);
          const sizesDescendantControl =
            attributeName === "className" &&
            descendantControlSizingPattern.test(value);
          const stylesControlledComponent =
            isControlledComponent &&
            attributeName === "style" &&
            inlineStyleSizingPattern.test(value);
          if (
            !sizesControlledComponent &&
            !sizesDescendantControl &&
            !stylesControlledComponent
          ) {
            continue;
          }

          context.report({
            node: attribute,
            messageId: "adHocSizing",
            data: { component, attribute: attributeName },
          });
        }
      },
    };
  },
};

export { noAdHocControlSizing };
