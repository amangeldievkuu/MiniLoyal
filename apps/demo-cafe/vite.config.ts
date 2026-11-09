import react from '@vitejs/plugin-react';
import { defaultClientConditions, defineConfig } from 'vite';

export default defineConfig({
  plugins: [react()],
  // "source" resolves @miniloyal/sdk to its TypeScript source in this monorepo (no build step).
  resolve: { conditions: ['source', ...defaultClientConditions] },
  server: {
    proxy: { '/v1': 'http://localhost:8080', '/healthz': 'http://localhost:8080' },
  },
});
