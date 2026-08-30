import { defineConfig, configDefaults } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // P3: the render-cost harness lives under src/perf/** and runs via
    // vitest.perf.config.ts only — keep it out of the unit suite.
    exclude: [...configDefaults.exclude, 'src/perf/**'],
  },
});
