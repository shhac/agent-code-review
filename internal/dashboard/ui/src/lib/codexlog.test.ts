import { describe, expect, it } from 'vitest';
import { parseCodexLog, verdictShaped, type ExecEvent } from './codexlog';

const join = (...lines: string[]) => lines.join('\n');

describe('parseCodexLog', () => {
  it('returns null for non-codex content so the raw view takes over', () => {
    expect(parseCodexLog('plain daemon output\nno markers here')).toBeNull();
  });

  it('splits banner, prompt, messages, and commands into events', () => {
    const events = parseCodexLog(
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
    const events = parseCodexLog(
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
    const events = parseCodexLog(join('exec', 'long-running-cmd in /wd'))!;
    const exec = events[0] as ExecEvent;
    expect(exec.ok).toBeUndefined();
    expect(exec.command).toBe('long-running-cmd in /wd');
  });

  it('keeps heredoc commands intact until the result line', () => {
    const events = parseCodexLog(
      join('exec', "zsh -lc 'cat <<EOF", 'line one', 'EOF', "' in /wd", ' succeeded in 3ms:'),
    )!;
    const exec = events[0] as ExecEvent;
    expect(exec.command).toContain('EOF');
    expect(exec.ok).toBe(true);
  });

  it('dedupes the repeated final message after the tokens trailer', () => {
    const final = '{"decision":"APPROVED","summary":"done"}';
    const events = parseCodexLog(join('codex', final, 'tokens used', '192,575', final))!;
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
