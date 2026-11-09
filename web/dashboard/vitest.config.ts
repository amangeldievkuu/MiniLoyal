import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // Node's Request needs absolute URLs; the browser build uses the same origin.
    env: { VITE_API_URL: 'http://api.test' },
  },
});
