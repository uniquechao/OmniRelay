import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { resolve } from 'node:path';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';

const upstream = process.env.OMNIRELAY_DEV_ORIGIN || 'http://127.0.0.1:8080';
const licenseRevision = createHash('sha256').update(readFileSync(new URL('./package-lock.json', import.meta.url))).digest('hex').slice(0, 8);
export default defineConfig({
  root: 'frontend',
  plugins: [react(), {
    name: 'setup-entry',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const url = new URL(req.url, 'http://localhost');
        if (req.method === 'GET' && req.headers.accept?.includes('text/html') && (url.pathname === '/setup' ||
            (url.pathname === '/' && url.searchParams.get('setup') === 'complete'))) {
          req.url = '/setup.html' + url.search;
        }
        next();
      });
    }
  }],
  server: { proxy: { '/api': upstream, '/auth': upstream, '^/setup(?:\\?|$)': upstream } },
  build: {
    outDir: '../internal/webui/dist', emptyOutDir: true, assetsInlineLimit: 0,
    license: {fileName: `assets/third-party-licenses-${licenseRevision}.txt`},
    rollupOptions: { input: [resolve('frontend/index.html'), resolve('frontend/setup.html')] }
  }
});
