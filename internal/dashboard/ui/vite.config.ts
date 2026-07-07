import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../assets',
    emptyOutDir: false,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8330',
    },
  },
});
