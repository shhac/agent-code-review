// Parser for the transcript a review engine tees into agent.log. `codex exec`
// prints this format natively; the claude driver renders its stream-json into
// the same shape (see internal/review/claudestream.go) so one parser serves
// every engine. The stream is a sequence of blocks introduced by bare marker
// lines:
//
//   user            the prompt handed to the agent
//   error           why a run ended without a report (claude only; codex has
//                   no equivalent, so the marker simply never appears)
//   thinking        a reasoning summary (absent when summaries are off)
//   codex | claude  an agent message, named for the engine that produced it
//   exec            a command; a " succeeded|exited|failed in <dur>:" line ends
//                   it and its output follows
//
// Parallel tool calls interleave: several exec markers can appear before any
// result line, then the results arrive together. Results carry no id, so
// they are paired with pending commands first-in-first-out: a best-effort
// read of an inherently ambiguous stream. Everything before the first marker
// is the session banner, and the ReviewLog page keeps a raw view as the
// ground truth.

export type ExecEvent = {
  kind: 'exec';
  command: string;
  output: string;
  // undefined while the command has no result line yet (still running, or
  // its result is unattributable in an interleaved section)
  ok?: boolean;
  duration?: string;
};

// The agent-message kinds, one per engine. Kept as a set so callers can ask
// "is this the agent talking?" without naming every engine.
export const agentKinds = ['codex', 'claude'] as const;
export type AgentKind = (typeof agentKinds)[number];

export function isAgentKind(kind: string): kind is AgentKind {
  return (agentKinds as readonly string[]).includes(kind);
}

export type LogEvent =
  | { kind: 'meta'; body: string }
  | { kind: 'user'; body: string }
  | { kind: 'thinking'; body: string }
  | { kind: AgentKind; body: string }
  | { kind: 'error'; body: string }
  | { kind: 'tokens'; body: string }
  | ExecEvent;

const markers = new Set(['user', 'thinking', ...agentKinds, 'exec', 'error', 'tokens used']);
const execResult = /^ (succeeded|exited|failed)\b.*?(?: in ([^\s:]+))?:?\s*$/;

// parseAgentLog splits the raw stream into events, or returns null when the
// content doesn't look like a codex exec stream (no markers) so the caller
// can fall back to the raw view.
export function parseAgentLog(raw: string): LogEvent[] | null {
  const lines = raw.split('\n');
  if (!lines.some((l) => markers.has(l))) return null;

  const events: LogEvent[] = [];
  // Prose blocks accumulate into `body`; exec blocks are event objects
  // mutated in place so interleaved results can attach to earlier commands.
  let kind: 'meta' | 'user' | 'thinking' | AgentKind | 'exec' | 'error' | 'tokens' = 'meta';
  let body: string[] = [];
  const pending: ExecEvent[] = []; // exec events awaiting a result line
  // Where a non-marker line lands while in an exec section: the one cursor.
  // An exec marker points it at the fresh event's command; a result line
  // repoints it at the completed event's output.
  let sink: { event: ExecEvent; field: 'command' | 'output' } | null = null;

  const flushProse = () => {
    const text = kind === 'tokens' ? dropRepeatedFinalMessage(body.join('\n').trim(), events) : body.join('\n').trim();
    body = [];
    if (kind !== 'exec' && text) events.push({ kind, body: text });
  };

  for (const line of lines) {
    if (markers.has(line)) {
      if (kind !== 'exec') flushProse();
      sink = null;
      kind = line === 'tokens used' ? 'tokens' : (line as 'user' | 'thinking' | AgentKind | 'exec' | 'error');
      if (kind === 'exec') {
        const ev: ExecEvent = { kind: 'exec', command: '', output: '' };
        events.push(ev);
        pending.push(ev);
        sink = { event: ev, field: 'command' };
      }
      continue;
    }
    if (kind !== 'exec') {
      body.push(line);
      continue;
    }
    const m = execResult.exec(line);
    if (m) {
      const done = pending.shift();
      if (done) {
        done.ok = m[1] === 'succeeded';
        done.duration = m[2];
        sink = { event: done, field: 'output' };
      } else if (sink?.field === 'command') {
        // Unattributable extra result line: whatever command it closed is
        // unknown, so stop attributing lines to the half-read command.
        sink = null;
      }
      continue;
    }
    if (sink) {
      sink.event[sink.field] += (sink.event[sink.field] ? '\n' : '') + line;
    }
  }
  flushProse();
  for (const ev of events) {
    if (ev.kind === 'exec') {
      ev.command = ev.command.trim();
      ev.output = ev.output.trim();
    }
  }
  return events;
}

// dropRepeatedFinalMessage trims the tokens trailer: the stream ends with
// "tokens used", the count, then a repeat of the final agent message; the
// repeat is already its own bubble, so only the count survives.
function dropRepeatedFinalMessage(text: string, events: LogEvent[]): string {
  const prev = [...events].reverse().find((e) => isAgentKind(e.kind));
  const [count, ...rest] = text.split('\n');
  if (prev && 'body' in prev && rest.join('\n').trim() === prev.body) return count;
  return text;
}

// verdictShaped extracts {decision, summary} from an agent message when the
// engine's output schema forced it into verdict JSON, so the page can render
// the summary as prose instead of a JSON blob.
export function verdictShaped(body: string): { decision: string; summary: string } | null {
  if (!body.startsWith('{')) return null;
  try {
    const v = JSON.parse(body);
    if (typeof v.decision === 'string' && typeof v.summary === 'string') return v;
  } catch {
    /* not JSON: render as-is */
  }
  return null;
}
