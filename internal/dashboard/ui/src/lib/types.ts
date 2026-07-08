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

// Header-badge tallies computed server-side (dashboard countQueue) — the one
// derivation both the Overview header and the QueueBoard section header use.
export type QueueCounts = {
  total: number;
  queued: number;
  reviewing: number;
  held: number;
};

export type Review = {
  repo: string;
  number: number;
  log_key?: string;
  title: string;
  author: string;
  verdict: string;
  engine: string;
  head_sha: string;
  reviewed_at: string;
  duration_secs: number;
  work_dir?: string;
  tokens_used?: number;
};

export type Run = {
  started_at: string;
  finished_at: string;
  status: string;
  host: string;
};

export type Bucket = {
  hour: string;
  approved: number;
  commented: number;
  requested_changes: number;
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

export type ConfigRepo = {
  name: string;
  allowed_authors_only?: boolean;
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
  version?: string;
};

export type AllowedAuthor = {
  repo: string;
  github_handle: string;
  name?: string;
  email?: string;
  slack_id?: string;
};
