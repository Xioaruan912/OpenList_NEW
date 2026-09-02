export type ThumbStatus = {
  cached_files: number
  local_files: number
  cloud_files: number
  fail_markers: number
  cache_size: number
  cache_dir: string
  prewarm_queued: number
  queue_paused: boolean
  prewarm_enabled: boolean
  auto_upload: boolean
  active_workers: number
  active_tasks?: { path: string; since: number }[]
  fail_items?: { path: string; dir: string; msg: string; at: string }[]
  blocked: boolean
  stale_by_dir?: { dir: string; count: number }[]
  mounts?: string[]
  metrics?: {
    cache_hits: number
    cache_misses: number
    cache_hit_rate: number
    placeholders: number
    generated: number
    generation_failed: number
    avg_generate_ms: number
    p95_generate_ms: number
    range_http: number
    range_reader: number
    range_gateway: number
    failures?: Record<string, number>
  }
}

export type ThumbCandidate = {
  index: number
  at: string
  png: string
}

export type CandidateJobState = "queued" | "running" | "succeeded" | "failed" | "canceled"

export type ThumbCandidatesData = {
  job_id?: string
  path?: string
  state?: CandidateJobState
  done?: number
  total?: number
  progress?: number
  error?: string
  created_at?: number
  candidates?: ThumbCandidate[]
  sheet?: string
  recommended_index?: number
  cached?: boolean
  risk_blocked?: boolean
  truncated?: boolean
}

export type CandidateJobSummary = Required<
  Pick<ThumbCandidatesData, "job_id" | "path" | "state" | "done" | "total" | "progress">
> &
  Pick<ThumbCandidatesData, "error" | "created_at">

export type TreeNode = {
  path: string
  name: string
  cached: number
  local?: number
  cloud?: number
  videos?: number
  children?: TreeNode[]
}

export type UploadStatus = {
  active: boolean
  paused: boolean
  queued: number
  remaining: number
  done: number
  failed: number
  exists: number
  fails: number
  total: number
  attempts?: number
  fail_items: { path: string; msg: string }[]
}

export type ThumbRuntime = {
  generation: {
    queued: number
    active: number
    paused: boolean
    blocked: boolean
    active_tasks?: { path: string; since: number }[]
  }
  upload: UploadStatus
  candidate_jobs: CandidateJobSummary[]
  tree: {
    scan_status: string
    refreshed_at: number
    stale: boolean
  }
}

export const emptyUploadStatus = (): UploadStatus => ({
  active: false,
  paused: false,
  queued: 0,
  remaining: 0,
  done: 0,
  failed: 0,
  exists: 0,
  fails: 0,
  total: 0,
  attempts: 0,
  fail_items: [],
})
