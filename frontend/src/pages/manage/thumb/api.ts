import { r } from "~/utils"

const base = "/admin/thumb"

export const thumbApi = {
  status: () => r.get(`${base}/status`),
  runtime: () => r.get(`${base}/runtime`),
  tree: () => r.get(`${base}/tree`),
  dir: (path: string) => r.get(`${base}/dir`, { params: { path } }),
  view: (path: string) => r.get(`${base}/view`, { params: { path }, responseType: "blob" }),

  setAuto: (generate?: boolean, upload?: boolean) => r.post(`${base}/auto`, { generate, upload }),
  generate: (path: string, recursive: boolean, force: boolean) =>
    r.post(`${base}/generate`, { path, recursive, force }),
  deletePaths: (paths: string[]) => r.post(`${base}/delete_paths`, { paths }),
  deleteFolder: (path: string) => r.post(`${base}/delete_folder`, { path }),
  clearAll: () => r.post(`${base}/clear_all`, {}),
  clearFails: () => r.post(`${base}/clear_fails`),
  retryFails: (path?: string) => r.post(`${base}/retry_fails`, path ? { path } : {}),
  exclude: (paths: string[], exclude: boolean) => r.post(`${base}/exclude`, { paths, exclude }),
  migrate: (oldPrefix: string, newPrefix: string) =>
    r.post(`${base}/migrate`, { old_prefix: oldPrefix, new_prefix: newPrefix }),

  queuePause: () => r.post(`${base}/queue/pause`, {}),
  queueResume: () => r.post(`${base}/queue/resume`, {}),
  queueClear: () => r.post(`${base}/queue/clear`, {}),

  upload: (path: string) => r.post(`${base}/upload`, { path }),
  uploadAll: () => r.post(`${base}/upload_all`, {}),
  uploadRetry: () => r.post(`${base}/upload_retry`, {}),
  uploadPause: () => r.post(`${base}/upload/pause`, {}),
  uploadResume: () => r.post(`${base}/upload/resume`, {}),
  uploadClear: () => r.post(`${base}/upload/clear`, {}),

  candidates: (path: string, refresh: boolean) => r.post(`${base}/candidates`, { path, refresh }),
  candidateStatus: (jobId: string) => r.get(`${base}/candidates/status`, { params: { job_id: jobId } }),
  candidateCancel: (jobId: string) => r.post(`${base}/candidates/cancel`, { job_id: jobId }),
  applyCandidate: (path: string, png: string) => r.post(`${base}/apply_candidate`, { path, png }),
}
