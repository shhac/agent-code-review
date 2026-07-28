import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { parseAgentLog, verdictShaped, type ExecEvent } from './agentlog';

const join = (...lines: string[]) => lines.join('\n');

describe('parseAgentLog', () => {
  it('returns null for non-codex content so the raw view takes over', () => {
    expect(parseAgentLog('plain daemon output\nno markers here')).toBeNull();
  });

  it('splits banner, prompt, messages, and commands into events', () => {
    const events = parseAgentLog(
      join(
        'OpenAI Codex v0.138.0',
        '--------',
        'user',
        'Review this PR',
        'codex',
        'starting now',
        'exec',
        "zsh -lc 'gh pr view 5' in /tmp/wd",
        ' succeeded in 484ms:',
        'PR title etc',
      ),
    )!;
    expect(events.map((e) => e.kind)).toEqual(['meta', 'user', 'codex', 'exec']);
    const exec = events[3] as ExecEvent;
    expect(exec.command).toContain('gh pr view 5');
    expect(exec.ok).toBe(true);
    expect(exec.duration).toBe('484ms');
    expect(exec.output).toBe('PR title etc');
  });

  it('pairs interleaved parallel results with pending commands FIFO', () => {
    // Two exec markers arrive before either result line: the shape the
    // stream takes when the agent runs tool calls in parallel.
    const events = parseAgentLog(
      join(
        'codex',
        'searching',
        'exec',
        'cmd-a in /wd',
        'exec',
        'cmd-b in /wd',
        ' succeeded in 866ms:',
        ' failed in 865ms:',
        'output for b',
        'codex',
        'done',
      ),
    )!;
    const execs = events.filter((e): e is ExecEvent => e.kind === 'exec');
    expect(execs).toHaveLength(2);
    expect(execs[0].command).toBe('cmd-a in /wd');
    expect(execs[0].ok).toBe(true);
    expect(execs[1].command).toBe('cmd-b in /wd');
    expect(execs[1].ok).toBe(false);
    expect(execs[1].output).toBe('output for b');
  });

  it('leaves a still-running command without a result', () => {
    const events = parseAgentLog(join('exec', 'long-running-cmd in /wd'))!;
    const exec = events[0] as ExecEvent;
    expect(exec.ok).toBeUndefined();
    expect(exec.command).toBe('long-running-cmd in /wd');
  });

  it('keeps heredoc commands intact until the result line', () => {
    const events = parseAgentLog(
      join('exec', "zsh -lc 'cat <<EOF", 'line one', 'EOF', "' in /wd", ' succeeded in 3ms:'),
    )!;
    const exec = events[0] as ExecEvent;
    expect(exec.command).toContain('EOF');
    expect(exec.ok).toBe(true);
  });

  it('dedupes the repeated final message after the tokens trailer', () => {
    const final = '{"decision":"APPROVED","summary":"done"}';
    const events = parseAgentLog(join('codex', final, 'tokens used', '192,575', final))!;
    const tokens = events.find((e) => e.kind === 'tokens')!;
    expect(tokens).toBeDefined();
    expect((tokens as { body: string }).body).toBe('192,575');
  });
});

describe('verdictShaped', () => {
  it('extracts schema-shaped agent messages', () => {
    expect(verdictShaped('{"decision":"WORKING","summary":"reading the diff"}')).toEqual({
      decision: 'WORKING',
      summary: 'reading the diff',
    });
  });

  it('passes prose and malformed JSON through', () => {
    expect(verdictShaped('plain prose message')).toBeNull();
    expect(verdictShaped('{"decision":7}')).toBeNull();
    expect(verdictShaped('{broken')).toBeNull();
  });
});

// The claude driver has no native transcript; it renders its stream-json into
// this same marker format (internal/review/claudestream.go) so one parser
// serves both engines. The fixture is written by that package's Go test, so
// if either side drifts, this fails instead of the review log silently
// degrading to a raw JSON dump. Regenerate with:
//   go test ./internal/review -update-golden
describe('claude transcripts render through the same parser', () => {
  const transcript = readFileSync(
    new URL('../../../../review/testdata/claude-transcript.golden', import.meta.url),
    'utf8',
  );

  it('parses the rendered claude transcript into events', () => {
    const events = parseAgentLog(transcript);
    expect(events).not.toBeNull();
    expect(events!.map((e) => e.kind)).toEqual([
      'meta',
      'user',
      'thinking',
      'exec',
      'exec',
      'claude',
      'tokens',
    ]);
  });

  it('pairs each command with its own result', () => {
    const execs = parseAgentLog(transcript)!.filter((e): e is ExecEvent => e.kind === 'exec');
    expect(execs.map((e) => [e.command, e.ok, e.duration])).toEqual([
      ['gh pr diff 42', true, '500ms'],
      ['Read {"file_path":"/tmp/wd/notes.md"}', false, '500ms'],
    ]);
    expect(execs[0].output).toContain('diff --git a/main.go');
    expect(execs[1].output).toBe('no such file');
  });

  it('renders the agent message as a verdict, not a JSON blob', () => {
    const agent = parseAgentLog(transcript)!.find((e) => e.kind === 'claude')!;
    expect(verdictShaped('body' in agent ? agent.body : '')).toEqual({
      decision: 'COMMENTED',
      summary: 'Left two inline notes about error handling.',
    });
  });

  it('keeps the token count and cost in the trailer', () => {
    const tokens = parseAgentLog(transcript)!.find((e) => e.kind === 'tokens')!;
    // The cost line rides along in the same block so a live log shows spend
    // without waiting for the review to land in history.
    expect('body' in tokens ? tokens.body : '').toBe('42,800\n~ $0.6231 at API rates');
  });
});
