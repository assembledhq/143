import { defineConfig, defineDocs } from "fumadocs-mdx/config";
import { metaSchema, pageSchema } from "fumadocs-core/source/schema";
import { z } from "zod";

const publicDocsFrontmatterSchema = pageSchema.extend({
  section: z.enum(["Get started", "Guides", "Self-hosting", "Reference"]),
  order: z.number().int().nonnegative(),
  status: z.enum(["draft", "beta", "stable"]),
  audience: z.enum(["evaluator", "engineer", "operator", "agent"]),
  tags: z.array(z.string()).default([]),
  llm_summary: z.string().min(12),
});

export const docs = defineDocs({
  dir: "../docs/public",
  docs: {
    schema: publicDocsFrontmatterSchema,
  },
  meta: {
    schema: metaSchema,
  },
});

export default defineConfig();
