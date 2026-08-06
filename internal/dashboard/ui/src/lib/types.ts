// Wire shapes returned by the dashboard's JSON API.

export type Candidate = {
  repo: string;
  number: number;
  url?: string;
  title: string;
  type: string;
  author: string;
  status: string;
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
};

export type Run = {
  started_at: string;
  finished_at: string;
  status: string;
  host: string;
};

export type ReviewsResponse = {
  reviews: Review[];
};

export type RunsResponse = {
  runs: Run[];
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
  summary: { reviews: number; fresh_tokens: number; cache_read_tokens: number; median_duration_secs: number; cost_usd: number; median_cost_usd: number; max_cost_usd: number; priced_reviews: number; estimated_reviews: number; check_reported_usd: number; check_estimated_usd: number; check_reviews: number };
  verdicts: Record<string, number>;
  activity: { day: string; reviews: number; fresh_tokens: number }[];
  models: { model: string; effort: string; engine_version: string; reviews: number; fresh_tokens: number; cache_read_tokens: number; median_duration_secs: number; median_cost_usd: number }[];
  scatter: { model: string; effort: string; verdict: string; fresh_tokens: number; duration_secs: number }[];
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
  reviewing_as?: string;
  repos: ConfigRepo[];
  candidates: {
    new_max_age_days: number;
    refreshed_max_age_days: number;
    rereview_cooldown: string;
    quiet_period: string;
  };
  schedule: {
    enabled: boolean;
    interval: string;
    max_parallel: number;
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

export type PromptResponse = {
  main_prompt?: string;
  outcomes?: Record<string, string>;
  rules?: Rule[];
  repos?: string[];
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
  state?: string;
  pr?: ReviewLogPr;
  work_dir?: string;
  size?: number;
  truncated?: boolean;
  content?: string;
  error?: string;
};
