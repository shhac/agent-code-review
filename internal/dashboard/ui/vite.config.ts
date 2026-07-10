import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createReadStream } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

const here = dirname(fileURLToPath(import.meta.url));
const mascotPath = resolve(here, '../assets/mascot.webp');

export default defineConfig({
  plugins: [
    svelte(),
    {
      name: 'dashboard-mascot-dev',
      configureServer(server) {
        server.middlewares.use('/mascot.webp', (_req, res) => {
          res.setHeader('Content-Type', 'image/webp');
          createReadStream(mascotPath).pipe(res);
        });
      },
    },
  ],
  build: {
    outDir: '../assets',
    emptyOutDir: false,
  },
  server: {
    proxy: {
      // Point the dev server at a running daemon's API. Defaults to the
      // standard local address; override to target a daemon on another port.
      '/api': process.env.ACR_API || 'http://127.0.0.1:8330',
    },
  },
});
