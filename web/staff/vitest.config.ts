import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // Node's Request needs absolute URLs; the browser build may use the same origin.
    env: {
      VITE_MINILOYAL_API: 'http://api.test',
      VITE_MINILOYAL_APP_ID: '0193a0b4-0000-7000-8000-000000000001',
      VITE_LIFF_ID: '2000000000-staffapp',
    },
  },
});
