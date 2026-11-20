import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { resolve } from 'node:path';

const upstream = process.env.OMNIRELAY_DEV_ORIGIN || 'http://127.0.0.1:8080';
export default defineConfig({
  root: 'frontend',
  plugins: [react(), {
    name: 'setup-entry',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const url = new URL(req.url, 'http://localhost');
        if (req.method === 'GET' && (url.pathname === '/setup' ||
            (url.pathname === '/' && url.searchParams.get('setup') === 'complete'))) {
          req.url = '/setup.html' + url.search;
        }
        next();
      });
    }
  }],
  server: { proxy: { '/api': upstream, '/auth': upstream, '^/setup(?:\\?|$)': upstream } },
  build: {
    outDir: '../web/dist', emptyOutDir: true, assetsInlineLimit: 0,
    rollupOptions: { input: [resolve('frontend/index.html'), resolve('frontend/setup.html')] }
  }
});
