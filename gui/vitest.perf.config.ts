import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// P3 (docs/design/adoption-lock.md): render-cost harness. Mirrors
// vitest.config.ts but includes ONLY the perf suite — the main config
// excludes src/perf/** so this gate never bleeds into the unit run.
// Gate: npx vitest run --config vitest.perf.config.ts
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/perf/**/*.perf.test.{ts,tsx}'],
  },
});
