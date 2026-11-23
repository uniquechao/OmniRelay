import { useProjectRevision } from './project-state'
import { Button } from './controls'
import { useEffect, useState } from 'react'
import { getProjectTaskEvidence } from './api'

export function ProjectTaskEvidence({ projectId, taskId }: { projectId: string; taskId: string }) {
  const stateRevision = useProjectRevision('tasks')
  const [open, setOpen] = useState(false)
  const [result, setResult] = useState<Awaited<ReturnType<typeof getProjectTaskEvidence>> | null>(null)
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    let sequence = 0
    const refresh = () => { const request = ++sequence; return getProjectTaskEvidence(projectId, taskId, controller.signal).then((data) => { if (!controller.signal.aborted && request === sequence) { setResult(data); setError('') } }).catch((cause) => { if (!controller.signal.aborted && request === sequence) { setResult(null); setError(cause instanceof Error ? cause.message : '来源依据读取失败。') } }) }
    void refresh()
    const timer = setInterval(() => void refresh(), 5000)
    return () => { clearInterval(timer); controller.abort() }
  }, [projectId, taskId, open, retry, stateRevision])
  return <details className="project-candidate-evidence" onToggle={(event) => { if (event.target !== event.currentTarget) return; setOpen(event.currentTarget.open); if (!event.currentTarget.open) setResult(null) }}><summary>查看任务来源依据</summary>{open && <>{error ? <div role="alert"><p>{error}</p><Button type="submit" variant="outline" onClick={() => setRetry((value) => value + 1)}>重新读取依据</Button></div> : result ? result.items.length ? result.items.map((item, index) => <div key={index}>{item.accessible ? item.evidence?.map((source) => <details key={source.id}><summary>{source.title || (source.kind === 'message' ? '聊天记录' : '会议资料')} · 来源时间 {new Date(source.created_at).toISOString().slice(0, 16).replace('T', ' ')} UTC</summary><pre>{source.text}</pre></details>) : <p>{item.reason || '当前账号无权读取原始依据。'}</p>}</div>) : <p>此任务没有关联的提炼依据。</p> : <p role="status">正在核验来源权限…</p>}</>}</details>
}
