// The e2e fixture: a real daemon, a real DuckDB store, and a stand-in for the
// `tailscale serve` proxy.
//
// Everything is scratch. The store is created under a temp XDG root and seeded
// directly rather than through the UI, so a spec never depends on another
// spec's writes and the suite cannot touch a developer's real queue.
import { execFileSync } from 'node:child_process';
import { mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

export const DAEMON_PORT = 18940;
export const PROXY_PORT = 18941;
export const ROOT = join(tmpdir(), 'acr-e2e');
export const CONFIG_HOME = join(ROOT, 'cfg');
export const DATA_HOME = join(ROOT, 'data');
export const DB = join(DATA_HOME, 'agent-code-review', 'queue.duckdb');
// The identity the proxy asserts. Matches the roster row seeded below.
export const VIEWER_LOGIN = 'octo@example.com';

const duck = (sql) => execFileSync('duckdb', [DB, '-c', sql], { stdio: 'pipe' });

// A 40-character head SHA and an estimated cost: the shape that produced the
// overlapping-text bug. A shorter SHA would have passed the broken build.
const SEED = `
INSERT INTO history
 (repo,number,title,author,head_sha,verdict,engine,model,effort,reviewed_at,duration_secs,tokens_used,cost_usd,est_cost_usd)
VALUES
 ('acme/widgets',22779,'feat(booking): generate entry QR codes via a queue and lambda','octocat',
  'c6bce3be1b32293bde57f0982026309b78e61403','APPROVED','codex','gpt-5.6-terra','medium',now(),300,2300000,0,0.8011),
 ('acme/widgets',22775,'fix(cx-widget): treat a default event as the subject','someone-else',
  'aeed56b594c59c73bb1a196e0026903d5e7a1d57','COMMENTED','codex','gpt-5.6-terra','medium',now(),240,1100000,0.4481,0);

INSERT INTO queue
 (repo,number,type,title,author,url,head_sha,created_at,updated_at,discovered_at,source,steering_message,steering_by,steering_at)
VALUES
 ('acme/widgets',101,'New','Add retry to the HTTP client','octocat','https://example.invalid/101','h1',
  now(),now(),now(),'manual',
  'The migration is behind a flag, so focus on:

- the **rollback** path
- the \`down\` migration','octocat',now()),
 ('acme/widgets',102,'New','Bump dependencies','someone-else','https://example.invalid/102','h2',
  now(),now(),now(),'manual',NULL,NULL,NULL);
`;

export function seed(bin) {
  rmSync(ROOT, { recursive: true, force: true });
  mkdirSync(CONFIG_HOME, { recursive: true });
  mkdirSync(DATA_HOME, { recursive: true });
  const env = { ...process.env, XDG_CONFIG_HOME: CONFIG_HOME, XDG_DATA_HOME: DATA_HOME };
  const run = (args) => execFileSync(bin, args, { env, stdio: 'pipe' });

  run(['config', 'init']);
  // gh_user is pinned so the operator rule is deterministic: without it the
  // daemon resolves the login through gh, which a test must not depend on.
  run(['config', 'set', 'gh_user', 'paul-gh']);
  run(['repos', 'add', 'acme/widgets']);
  run(['authors', 'set', '*', 'octocat', 'approver', '--tailscale-login', VIEWER_LOGIN]);
  run(['authors', 'set', '*', 'paul-gh', 'approver', '--tailscale-login', 'paul@example.com']);
  // Opening the store applies the schema; seed after that.
  run(['queue', 'ls']);
  duck(SEED);
}
