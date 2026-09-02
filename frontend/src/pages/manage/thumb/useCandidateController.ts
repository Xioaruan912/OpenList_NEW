import { createSignal } from "solid-js"
import { handleResp, handleRespWithoutNotify, notify } from "~/utils"
import { thumbApi } from "./api"
import type { CandidateJobSummary, ThumbCandidate, ThumbCandidatesData } from "./types"

type CandidateControllerOptions = {
  selectedDir: () => string
  onApplied: (path: string) => void
}

export const useCandidateController = (options: CandidateControllerOptions) => {
  const [viewPath, setViewPath] = createSignal("")
  const [viewUrl, setViewUrl] = createSignal("")
  const [viewLoading, setViewLoading] = createSignal(false)
  const [candidates, setCandidates] = createSignal<ThumbCandidate[]>([])
  const [loading, setLoading] = createSignal(false)
  const [sheet, setSheet] = createSignal("")
  const [recommendedIndex, setRecommendedIndex] = createSignal(0)
  const [cached, setCached] = createSignal(false)
  const [riskBlocked, setRiskBlocked] = createSignal(false)
  const [truncated, setTruncated] = createSignal(false)
  const [jobId, setJobId] = createSignal("")
  const [progress, setProgress] = createSignal(0)
  const [applying, setApplying] = createSignal(false)
  const [jobs, setJobs] = createSignal<CandidateJobSummary[]>([])

  let requestId = 0
  let requestActive = false
  let jobsLoaded = false
  let previousJobStates = new Map<string, string>()

  const revokeViewUrl = () => {
    if (viewUrl()) URL.revokeObjectURL(viewUrl())
    setViewUrl("")
  }

  // Detach only. A candidate task is backend-owned and keeps running unless the user explicitly
  // presses Cancel. This is the core guarantee that allows users to leave the modal/page.
  const resetLocalState = () => {
    requestId += 1
    requestActive = false
    setJobId("")
    setCandidates([])
    setSheet("")
    setRecommendedIndex(0)
    setCached(false)
    setRiskBlocked(false)
    setTruncated(false)
    setProgress(0)
    setLoading(false)
    setApplying(false)
  }

  const applyJobs = (incoming: CandidateJobSummary[]) => {
    const latest = incoming.slice(0, 8)
    if (jobsLoaded) {
      for (const job of latest) {
        const previous = previousJobStates.get(job.job_id)
        if (previous && previous !== "succeeded" && job.state === "succeeded") {
          notify.success(`3×3 候选已完成：${job.path.split("/").pop() || job.path}`)
        }
        if (previous && previous !== "failed" && job.state === "failed") {
          notify.error(`3×3 候选失败：${job.path.split("/").pop() || job.path}`)
        }
      }
    }
    previousJobStates = new Map(latest.map((job) => [job.job_id, job.state]))
    jobsLoaded = true
    setJobs(latest)
  }

  const applyResponse = (
    data: ThumbCandidatesData,
    refresh: boolean,
    previousCandidates: ThumbCandidate[],
  ) => {
    const next = data.candidates || []
    if (refresh && !next.length && previousCandidates.length) {
      notify.warning("重新生成未取得新画面，已保留上次候选")
      return
    }
    setCandidates(next)
    setSheet(data.sheet || "")
    setRecommendedIndex(data.recommended_index || 0)
    setCached(!!data.cached)
    setRiskBlocked(!!data.risk_blocked)
    setTruncated(!!data.truncated)
    setProgress(100)
    if (!next.length) {
      if (data.risk_blocked || data.truncated) {
        notify.warning("为避免触发 115 风控，候选生成已停止，暂无可用画面")
      } else {
        notify.error("未能生成候选缩略图")
      }
    } else if (data.risk_blocked || data.truncated) {
      notify.warning(`已取得 ${next.length} 个画面，后续取帧已停止以避免触发 115 风控`)
    }
  }

  const pollJob = async (
    currentJobId: string,
    currentRequestId: number,
    path: string,
    refresh: boolean,
    previousCandidates: ThumbCandidate[],
  ) => {
    while (currentRequestId === requestId && viewPath() === path && jobId() === currentJobId) {
      await new Promise((resolve) => setTimeout(resolve, 750))
      if (currentRequestId !== requestId || viewPath() !== path || jobId() !== currentJobId) return
      try {
        const resp = await thumbApi.candidateStatus(currentJobId)
        let data: ThumbCandidatesData | undefined
        handleRespWithoutNotify(resp, (value) => {
          data = value as ThumbCandidatesData
        })
        if (!data) continue
        setProgress(data.progress || 0)
        if (data.state === "succeeded") {
          applyResponse(data, refresh, previousCandidates)
          setJobId("")
          setLoading(false)
          requestActive = false
          return
        }
        if (data.state === "failed" || data.state === "canceled") {
          setJobId("")
          setLoading(false)
          requestActive = false
          if (data.state === "failed") notify.error(data.error || "生成候选缩略图失败")
          return
        }
      } catch {
        if (currentRequestId === requestId && viewPath() === path) {
          notify.error("读取候选生成进度失败")
          setLoading(false)
          setJobId("")
        }
        requestActive = false
        return
      }
    }
  }

  const generate = async (path: string, refresh = false) => {
    if (requestActive) return
    requestActive = true
    const previousCandidates = candidates()
    const currentRequestId = ++requestId
    setLoading(true)
    setProgress(0)
    if (!refresh) {
      setSheet("")
      setRecommendedIndex(0)
      setCached(false)
      setRiskBlocked(false)
      setTruncated(false)
      setCandidates([])
    }
    try {
      const resp = await thumbApi.candidates(path, refresh)
      if (currentRequestId !== requestId || viewPath() !== path) return
      handleResp(resp, (value) => {
        if (currentRequestId !== requestId || viewPath() !== path) return
        const data = value as ThumbCandidatesData
        if (data.state === "succeeded" || data.candidates) {
          applyResponse(data, refresh, previousCandidates)
          setLoading(false)
          requestActive = false
          return
        }
        if (data.job_id) {
          setJobId(data.job_id)
          setProgress(data.progress || 0)
          void pollJob(data.job_id, currentRequestId, path, refresh, previousCandidates)
        }
      })
    } catch {
      if (currentRequestId !== requestId || viewPath() !== path) return
      notify.error(refresh && previousCandidates.length ? "重新生成失败，已保留上次候选" : "生成候选缩略图失败")
    } finally {
      if (currentRequestId === requestId && viewPath() === path && !jobId()) {
        setLoading(false)
        requestActive = false
      }
    }
  }

  const view = async (path: string) => {
    revokeViewUrl()
    setViewPath(path)
    setViewLoading(true)
    resetLocalState()
    try {
      const resp = await thumbApi.view(path)
      if (resp instanceof Blob && resp.type.startsWith("image/")) {
        setViewUrl(URL.createObjectURL(resp))
      } else {
        notify.error("无法查看缩略图（未生成或生成失败）")
        setViewPath("")
      }
    } catch {
      notify.error("查看缩略图失败")
      setViewPath("")
    } finally {
      setViewLoading(false)
    }
  }

  const openGenerator = (path: string) => {
    if (requestActive) return
    revokeViewUrl()
    setViewPath(path)
    setViewLoading(false)
    resetLocalState()
    void generate(path)
  }

  const openJob = async (job: CandidateJobSummary) => {
    revokeViewUrl()
    resetLocalState()
    setViewPath(job.path)
    setViewLoading(true)
    try {
      const preview = await thumbApi.view(job.path).catch(() => null)
      if (preview instanceof Blob && preview.type.startsWith("image/")) {
        setViewUrl(URL.createObjectURL(preview))
      }
      const resp = await thumbApi.candidateStatus(job.job_id)
      handleResp(resp, (value) => {
        const data = value as ThumbCandidatesData
        if (data.state === "succeeded") applyResponse(data, false, [])
        else notify.info("候选任务尚未完成")
      })
    } finally {
      setViewLoading(false)
    }
  }

  const cancelJob = async (candidateJob: CandidateJobSummary) => {
    try {
      const resp = await thumbApi.candidateCancel(candidateJob.job_id)
      handleRespWithoutNotify(resp, () => {
        if (jobId() === candidateJob.job_id) resetLocalState()
        setJobs((current) =>
          current.map((job) =>
            job.job_id === candidateJob.job_id ? { ...job, state: "canceled" as const } : job,
          ),
        )
        notify.success("已取消候选任务")
      })
    } catch {
      // The 2s control-plane poll will reconcile a race with task completion.
    }
  }

  const cancelCurrent = async () => {
    const currentJobId = jobId()
    if (!currentJobId) return
    requestId += 1
    requestActive = false
    setJobId("")
    setLoading(false)
    try {
      const resp = await thumbApi.candidateCancel(currentJobId)
      handleRespWithoutNotify(resp, () => notify.success("已取消候选生成"))
    } catch {
      // It may already have completed.
    }
  }

  const apply = async (path: string, png: string, successMessage = "已应用所选缩略图") => {
    setApplying(true)
    try {
      const resp = await thumbApi.applyCandidate(path, png)
      handleResp(resp, () => {
        notify.success(successMessage)
        close()
        options.onApplied(options.selectedDir() || path)
      })
    } finally {
      setApplying(false)
    }
  }

  const close = () => {
    revokeViewUrl()
    setViewPath("")
    setViewLoading(false)
    resetLocalState()
  }

  const dispose = () => {
    requestId += 1
    revokeViewUrl()
  }

  return {
    viewPath,
    viewUrl,
    viewLoading,
    candidates,
    loading,
    sheet,
    recommendedIndex,
    cached,
    riskBlocked,
    truncated,
    jobId,
    progress,
    applying,
    jobs,
    applyJobs,
    view,
    openGenerator,
    openJob,
    cancelJob,
    cancelCurrent,
    generate,
    apply,
    close,
    dispose,
  }
}
