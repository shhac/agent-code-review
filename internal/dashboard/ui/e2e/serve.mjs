// Builds and starts the daemon against the seeded scratch store.
import { execFileSync, spawn } from 'node:child_process';
import { join } from 'node:path';
import { CONFIG_HOME, DAEMON_PORT, DATA_HOME, seed } from './fixture.mjs';

const repo = join(import.meta.dirname, '..', '..', '..', '..');
const bin = join(repo, 'agent-code-review');
// Always rebuild BOTH halves. `make build` is go build alone: the UI is a
// separate `make dashboard` target that writes internal/dashboard/assets,
// which dashboard.go go:embeds. Building only the Go half serves whatever CSS
// and markup was compiled last, so an edit under src/ is invisible to the
// suite. Skipping either step made deliberately broken builds pass.
execFileSync('make', ['dashboard', 'build'], { cwd: repo, stdio: 'inherit' });

seed(bin);

const child = spawn(bin, ['serve', '--http', `127.0.0.1:${DAEMON_PORT}`, '--no-discovery', '--no-reviews'], {
  env: { ...process.env, XDG_CONFIG_HOME: CONFIG_HOME, XDG_DATA_HOME: DATA_HOME },
  stdio: 'inherit',
});
process.on('SIGTERM', () => child.kill('SIGTERM'));
process.on('SIGINT', () => child.kill('SIGINT'));
