import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/v1': 'http://localhost:8080', '/healthz': 'http://localhost:8080' },
  },
});
