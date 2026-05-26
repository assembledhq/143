import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import path from 'path';

const nodeTestFiles = [
  'src/ci-guardrails.test.ts',
  'src/next-config.test.ts',
  'src/lib/agents.test.ts',
  'src/lib/automation-templates.test.ts',
  'src/lib/coding-agent-reasoning.test.ts',
  'src/lib/diff-parser.test.ts',
  'src/lib/docs/public-docs.test.ts',
  'src/lib/errors.test.ts',
  'src/lib/format-review-message.test.ts',
  'src/lib/integrations.test.ts',
  'src/lib/model-constants.test.ts',
  'src/lib/query-keys.test.ts',
  'src/lib/session-composer-mentions.test.ts',
  'src/lib/session-open-position.test.ts',
  'src/lib/source.test.ts',
  'src/lib/sse.test.ts',
  'src/lib/syntax-highlighter.test.ts',
  'src/lib/timeline.test.ts',
  'src/lib/tool-label.test.ts',
  'src/lib/use-eval-sse.test.ts',
  'src/lib/utils.test.ts',
  'src/components/autopilot/autopilot-helpers.test.ts',
  'src/components/command-palette/command-palette-actions.test.ts',
  'src/components/code-review/index.test.ts',
  'src/app/(dashboard)/automations/[id]/automation-stats-card.test.tsx',
  'src/app/(dashboard)/automations/[id]/run-grouping.test.ts',
  'src/app/(dashboard)/sessions/[id]/session-detail-content.test.ts',
];

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      'collections': path.resolve(__dirname, './.source'),
    },
  },
  test: {
    globals: true,
    css: false,
    testTimeout: 5_000,
    hookTimeout: 10_000,
    reporters: process.env.CI ? ['dot'] : ['default'],
    projects: [
      {
        extends: true,
        test: {
          name: 'node',
          environment: 'node',
          include: nodeTestFiles,
        },
      },
      {
        extends: true,
        test: {
          name: 'jsdom',
          environment: 'jsdom',
          setupFiles: ['./src/test/setup.ts'],
          include: ['src/**/*.test.{ts,tsx}'],
          exclude: nodeTestFiles,
        },
      },
    ],
  },
});
