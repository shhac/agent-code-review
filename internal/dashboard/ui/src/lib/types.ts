// Wire shapes returned by the dashboard's JSON API.

// The three states a queued row can be in, mirroring dashboard.claimStatus:
// a live claim is "reviewing", a hold in the future is "held", anything else
// is "queued". countQueue on the server emits exactly these and asserts they
// sum to the total, so the client having its own opinion was never the plan —
// it just had no way to say so, and compared the literals at ten sites instead.
//
// This is a claim about the CURRENT server, which is why the components keep a
// fallback arm: a deploy can briefly make it false, and rendering something
// unrecognised beats rendering nothing.
export type QueueStatus = 'queued' | 'reviewing' | 'held';

// ReviewLogState is the same vocabulary plus the state a row reaches once it
// has left the queue. The log page is the one view that outlives the row.
export type ReviewLogState = QueueStatus | 'finished';

export type Candidate = {
  repo: string;
  number: number;
  url?: string;
  title: string;
  type: string;
  author: string;
  status: QueueStatus;
  head_sha: string;
  queue_pos: number;
  created_at: string;
  updated_at: string;
  discovered_at: string;
  claimed_at?: string;
  source: string;
  work_dir?: string;
  eligible_at?: string;
  hold_reason?: string;
  steering?: Steering;
  // Whether the current viewer may steer this PR, decided server-side.
  may_steer?: boolean;
};

// Header-badge tallies computed server-side (dashboard countQueue): the one
// derivation both the Overview header and the QueueBoard section header use.
export type QueueCounts = {
  total: number;
  queued: number;
  reviewing: number;
  held: number;
};

export type QueueResponse = {
  candidates: Candidate[];
  counts: QueueCounts;
};

export type Review = {
  repo: string;
  number: number;
  log_key?: string;
  title: string;
  author: string;
  verdict: string;
  engine: string;
  model?: string;
  effort?: string;
  engine_version?: string;
  head_sha: string;
  reviewed_at: string;
  duration_secs: number;
  cost_usd?: number;
  // True when cost_usd is our valuation rather than the engine's, so the UI
  // can label it instead of presenting an inference as a measurement.
  cost_estimated?: boolean;
  work_dir?: string;
  tokens_used?: number;
  // The instruction this review was given, copied off the queue row as it was
  // retired. ABSENT MEANS NOT RECORDED, not "not steered": every row written
  // before history kept a copy reads absent whatever it was told, and those
  // messages are gone. Render it when present; never render its absence as
  // evidence that no instruction was given.
  steering?: Steering;
  // What this review earned the PR's author, and the size tier it came from.
  // ABSENT means unscored, which is not the same as 0: a PR whose every line
  // was generated legitimately earns nothing. Render the two differently.
  score?: number;
  score_bucket?: string;
};

export type ReviewsResponse = {
  reviews: Review[];
  // Rows matching the query across the whole table, not just this page.
  total: number;
  // Opaque cursor for the next page, absent on the last one.
  next_cursor?: string;
};

export type Bucket = {
  hour: string;
  approved: number;
  commented: number;
  requested_changes: number;
};

export type StatsResponse = {
  buckets: Bucket[];
};

export type MetricsResponse = {
  summary: { reviews: number; outcomes: number; fresh_tokens: number; cache_read_tokens: number; median_duration_secs: number; cost_usd: number; median_cost_usd: number; max_cost_usd: number; priced_reviews: number; estimated_reviews: number; check_reported_usd: number; check_estimated_usd: number; check_reviews: number };
  verdicts: Record<string, number>;
  activity: { day: string; reviews: number; fresh_tokens: number }[];
  // One row per model+effort, with each CLI version's share nested. The
  // row's medians are computed over all its reviews, not averaged from the
  // versions below: a median of medians is not a median.
  models: {
    model: string;
    effort: string;
    reviews: number;
    fresh_tokens: number;
    cache_read_tokens: number;
    median_duration_secs: number;
    median_cost_usd: number;
    versions: { engine_version: string; reviews: number; fresh_tokens: number; cache_read_tokens: number; median_duration_secs: number; median_cost_usd: number }[];
  }[];
  scatter: { model: string; effort: string; verdict: string; fresh_tokens: number; duration_secs: number }[];
};

// ScoringMode is the global switch. leaderboard-only stops the per-review
// GitHub call that measures a diff, without hiding the points already earned:
// stopping the work and hiding the results are separate decisions.
export type ScoringMode = 'enabled' | 'leaderboard-only' | 'disabled';

export type LeaderboardEntry = {
  rank: number;
  author: string;
  name?: string;
  total: number;
  reviews: number;
  approvals: number;
  additions: number;
  deletions: number;
};

export type LeaderboardResponse = {
  enabled: boolean;
  mode: ScoringMode;
  days: number;
  repo?: string;
  entries: LeaderboardEntry[];
};

export type UsageWindow = {
  window_mins: number;
  used_percent: number;
  resets_at?: number;
};

export type UsageSnapshot = {
  error?: string;
  plan?: string;
  fetched_at?: string;
  primary?: UsageWindow;
  secondary?: UsageWindow;
};

// One engine's meter. `available` is not derivable from the snapshot: a failed
// poll still stamps fetched_at, so the server decides, and `error` explains an
// unavailable engine rather than leaving a blank meter.
export type EngineUsage = {
  engine: string;
  active: boolean;
  available: boolean;
  error?: string;
  usage?: UsageSnapshot;
};

export type UsageResponse = {
  available: boolean;
  // The configured engine, so the panel knows which slot is live.
  engine?: string;
  // Every metered engine, so the one NOT in use can be compared against.
  engines?: EngineUsage[];
  review_paused?: boolean;
  paused_reason?: string;
  fresh_tokens_total?: number;
  fresh_tokens_24h?: number;
};

export type ConfigRepo = {
  name: string;
  // True when an author with no roster row is not discovered here, derived
  // from the repo's unlisted policy rather than a separate setting.
  allowed_authors_only?: boolean;
  unlisted_group?: string;
};

export type ConfigResponse = {
  scoring: {
    mode: ScoringMode;
    leaderboard_visible: boolean;
    base: number;
    churn_unit: number;
    deletion_weight: number;
    approved: number;
    commented: number;
    requested_changes: number;
    shrink_bonus: number;
    attempt_decay: number;
    use_gitattributes: boolean;
    exclude_paths: number;
  };
  workspace_retention: string;
  reviewing_as?: string;
  repos: ConfigRepo[];
  candidates: {
    new_max_age_days: number;
    refreshed_max_age_days: number;
    discussion_max_age_days: number;
    rereview_cooldown: string;
    quiet_period: string; steering_hold: string; error_backoff: string;
  };
  schedule: {
    enabled: boolean;
    interval: string;
    max_parallel: number; dispatch_cooldown: string;
    usage_floor_5h_percent: number;
    usage_floor_weekly_percent: number;
  };
  discovery: {
    enabled: boolean;
    interval: string;
  };
  review_running: boolean;
  discovery_running: boolean;
  engine: string;
  engine_config: {
    model: string;
    effort: string;
  };
  version?: string;
};

export type AllowedAuthor = {
  repo: string;
  github_handle: string;
  group: string;
  name?: string;
  email?: string;
  slack_id?: string;
  // What the group actually resolves to, sent with the row because a group
  // config no longer defines is invisible otherwise.
  policy?: AuthorPolicy;
};

export type AuthorPolicy = {
  group: string;
  review: string; // "ignore" | "comment" | "approve"
  engine?: string;
  model?: string;
  effort?: string;
  prompt?: string;
};

export type AuthorsResponse = {
  authors: AllowedAuthor[];
};

export type RuleCondition = {
  author_is_gh_user?: boolean;
  author_not_gh_user?: boolean;
  author_allowed?: boolean;
  author_not_allowed?: boolean;
  groups?: string[];
  authors?: string[];
  candidate_type?: string;
  repos?: string[];
  outcome?: string;
};

export type Rule = { name: string; prompt: string; when?: RuleCondition };

export type PromptGroup = {
  name: string;
  review: string; // ignore | comment | approve
  builtin: boolean;
};

export type PromptResponse = {
  main_prompt?: string;
  outcomes?: Record<string, string>;
  rules?: Rule[];
  repos?: string[];
  groups?: PromptGroup[];
  note?: string;
};

export type RuleTrace = {
  name: string;
  target: string; // "body" | "approve" | "comment" | "reject"
  matched: boolean;
  reason?: string;
};

export type PolicyStep = {
  field: string;
  value: string;
  source: string;
};

export type PromptPreviewResponse = {
  candidate: {
    repo: string;
    candidate_type: string;
    author: string;
    group: string;
    author_allowed: boolean;
    author_is_gh_user: boolean;
  };
  preview: string;
  policy: PolicyStep[];
  rules: RuleTrace[];
};

export type LogEntry = {
  at: string;
  line: string;
};

export type LogsResponse = {
  available: boolean;
  entries: LogEntry[];
};

export type ReviewLogPr = {
  repo: string;
  number: number;
  title?: string;
  author?: string;
  url?: string;
  verdict?: string;
  claimed_at?: string;
  reviewed_at?: string;
  duration_secs?: number;
  tokens_used?: number;
  // The split behind tokens_used. Always sent: cache_read_tokens 0 is a fact
  // about the review (nothing was re-read), not a gap in the response.
  fresh_tokens: number;
  cache_read_tokens: number;
  cost_usd?: number;
  cost_estimated?: boolean;
};

export type ReviewLogRef = {
  repo: string;
  number: number;
  logKey?: string;
};

export type ReviewLogResponse = {
  available: boolean;
  state?: ReviewLogState;
  pr?: ReviewLogPr;
  work_dir?: string;
  size?: number;
  truncated?: boolean;
  content?: string;
  error?: string;
};

// Steering is an instruction from the PR's author (or from the account
// reviews are posted as) that shapes the next review of that PR.
export type Steering = {
  message: string;
  set_by: string;
  set_at: string;
};

// SteeringHold is the editing hold's answer, as one named state rather than a
// pair of optionals the client has to recombine. Only `held` means the PR is
// actually parked, and only `held` is worth asking again about: `capped` and
// `disabled` both mean stop renewing, for different reasons.
export type SteeringHoldState = 'held' | 'capped' | 'released' | 'disabled';

export type SteeringHold = {
  state: SteeringHoldState;
  until?: string;
};

// QueueAdd is the add's answer. steering_refused says why an accompanying
// message was not applied: the add still happened, so the caller has to be
// told which half of what they asked for they got.
export type QueueAdd = {
  queued: boolean;
  title?: string;
  author?: string;
  steered?: boolean;
  steering_refused?: string;
};

// QueuePreflight answers "what is this PR, and may I steer it" before an add.
export type QueuePreflight = {
  repo: string;
  number: number;
  title: string;
  author: string;
  may_steer: boolean;
  // The server's wording for why not, when may_steer is false. Rendered as
  // sent: the client does not keep its own copy of the sentence.
  refusal?: string;
};

// ViewerState is how the dashboard knows the caller. The server classifies;
// the client only chooses words and colours for each case.
export type ViewerState = 'anonymous' | 'unmapped' | 'author' | 'operator';

// Viewer is who the dashboard believes is asking, derived from the identity
// `tailscale serve` attaches. `handle` is empty for someone the roster does
// not claim: authenticated, but owning no PRs here. Whether a PARTICULAR PR is
// steerable is not here: the queue answers that per row via may_steer.
export type Viewer = {
  state: ViewerState;
  login?: string;
  handle?: string;
};
