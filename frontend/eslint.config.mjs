import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

// ---------------------------------------------------------------------------
// Custom rule: no-banned-typography
// Enforces the project's standardised type scale. Only `text-[13px]` is
// permitted as an arbitrary pixel size — everything else must use the Tailwind
// scale (text-xs, text-sm, text-lg, text-2xl, etc.).
// ---------------------------------------------------------------------------
const ALLOWED_PIXEL_SIZES = new Set(["13"]);

const noBannedTypography = {
  meta: {
    type: "suggestion",
    docs: {
      description:
        "Disallow banned typography Tailwind classes to enforce the standard type scale",
    },
    schema: [],
    messages: {
      bannedFontSize:
        'Banned font size "{{cls}}". Use text-xs (12px) or text-sm (14px) instead. Only text-[13px] is allowed as an arbitrary pixel size.',
      bannedFontBold:
        "Use font-semibold instead of font-bold.",
      bannedTrackingWidest:
        "Use tracking-wider instead of tracking-widest.",
      bannedTextXl:
        "Use text-lg instead of text-xl.",
    },
  },
  create(context) {
    const PIXEL_RE = /\btext-\[(\d+)px\]/g;
    const BOLD_RE = /\bfont-bold\b/;
    const TRACKING_RE = /\btracking-widest\b/;
    const TEXT_XL_RE = /\btext-xl\b/;

    function check(node, value) {
      let m;
      PIXEL_RE.lastIndex = 0;
      while ((m = PIXEL_RE.exec(value)) !== null) {
        if (!ALLOWED_PIXEL_SIZES.has(m[1])) {
          context.report({ node, messageId: "bannedFontSize", data: { cls: m[0] } });
        }
      }
      if (BOLD_RE.test(value)) {
        context.report({ node, messageId: "bannedFontBold" });
      }
      if (TRACKING_RE.test(value)) {
        context.report({ node, messageId: "bannedTrackingWidest" });
      }
      if (TEXT_XL_RE.test(value)) {
        context.report({ node, messageId: "bannedTextXl" });
      }
    }

    return {
      Literal(node) {
        if (typeof node.value === "string") check(node, node.value);
      },
      TemplateLiteral(node) {
        for (const quasi of node.quasis) {
          check(node, quasi.value.raw);
        }
      },
    };
  },
};

const customPlugin = {
  rules: {
    "no-banned-typography": noBannedTypography,
  },
};

// ---------------------------------------------------------------------------
// Main config
// ---------------------------------------------------------------------------
const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    plugins: { custom: customPlugin },
    rules: {
      "custom/no-banned-typography": "error",
    },
  },
  {
    files: ["eslint.config.mjs"],
    rules: {
      "custom/no-banned-typography": "off",
    },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    ".source/**",
    "out/**",
    "build/**",
    "coverage/**",
    "next-env.d.ts",
  ]),
]);

export default eslintConfig;
