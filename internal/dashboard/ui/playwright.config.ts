import { defineConfig } from '@playwright/test';
import { CONFIG_HOME, DAEMON_PORT, DATA_HOME, PROXY_PORT } from './e2e/fixture.mjs';

// The suite runs against the REAL daemon and a real DuckDB store, because the
// bugs it exists to catch (a stylesheet rule reaching into the wrong subtree,
// a grid whose columns stopped lining up) only appear once the assembled page
// is laid out by a browser. jsdom cannot see them: it has no layout engine, so
// every getBoundingClientRect is zero.
export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.ts',
  fullyParallel: false, // one daemon, one store
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0, // these assertions are deterministic; a retry would hide a real flake
  reporter: process.env.CI ? [['github'], ['list']] : [['list']],
  use: {
    baseURL: `http://127.0.0.1:${PROXY_PORT}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  // Never reuse a running server: the daemon EMBEDS the built UI, so one left
  // over from a previous run serves the previous run's CSS. That silently made
  // three deliberately broken builds pass.
  webServer: [
    {
      // --no-discovery --no-reviews: the daemon must never call gh or start an
      // engine. A test suite that can spend LLM budget is not a test suite.
      command: `node e2e/serve.mjs`,
      url: `http://127.0.0.1:${DAEMON_PORT}/api/healthz`,
      reuseExistingServer: false,
      stdout: 'pipe',
      stderr: 'pipe',
      env: { XDG_CONFIG_HOME: CONFIG_HOME, XDG_DATA_HOME: DATA_HOME },
    },
    {
      command: 'node e2e/proxy.mjs',
      url: `http://127.0.0.1:${PROXY_PORT}/api/healthz`,
      reuseExistingServer: false,
    },
  ],
});
