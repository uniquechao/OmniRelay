import { Textarea } from './controls'
import { NativeSelect } from './controls'
import { Button } from './controls'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ProjectDialog } from './ProjectDialog'
import { getTaskFeedback, reviewTaskFeedback, submitTaskFeedback, type ProjectDetail, type ProjectTask, type TaskFeedback } from './api'

const labels = { in_progress: '进行中', blocked: '受阻', done: '已完成' }
export function ProjectTaskFeedback({ project, task, canManage, onChanged }: { project: ProjectDetail; task: ProjectTask; canManage: boolean; onChanged: () => void }) {
  const [data, setData] = useState<Awaited<ReturnType<typeof getTaskFeedback>> | null>(null)
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [submitOpen, setSubmitOpen] = useState(false)
  const [review, setReview] = useState<{ item: TaskFeedback; action: 'accept' | 'reject' } | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    getTaskFeedback(project.id, task.id, controller.signal).then((value) => { setData(value); setError('') }).catch((cause) => { if (!controller.signal.aborted) { setData(null); setError(cause instanceof Error ? cause.message : '进展反馈加载失败。') } })
    return () => controller.abort()
  }, [project.id, task.id, task.version, retry])
  return <section className="project-task-feedback"><h4>进展反馈与审核</h4>{data?.can_submit && <Button type="submit" variant="outline" onClick={() => setSubmitOpen(true)}>提交本人进展</Button>}{data && !data.can_submit && !canManage && <p className="field-hint">当前任务暂不支持提交本人进展，请核对任务主责与参与关系。</p>}{error && <div role="alert"><p>{error}</p><Button type="submit" variant="outline" onClick={() => setRetry((value) => value + 1)}>重新加载反馈</Button></div>}{data && <ul className="project-task-history">{data.items.map((item) => <li key={item.id}><strong>{labels[item.proposed_status]}建议 · {item.status === 'pending' ? '待审核' : item.status === 'accepted' ? '已采用' : '已拒绝'}</strong><span>{project.members.find((member) => member.id === item.actor_id)?.display_name ?? item.actor_id} · 基于任务 v{item.task_version}</span><p>{item.note}</p>{item.reason && <p>审核说明：{item.reason}</p>}{canManage && item.status === 'pending' && <div className="project-candidate-actions"><Button type="submit" variant="outline" onClick={() => setReview({ item, action: 'accept' })}>审核并采用进展</Button><Button type="submit" variant="ghost" onClick={() => setReview({ item, action: 'reject' })}>拒绝进展建议</Button></div>}</li>)}</ul>}{data && !data.items.length && <p>暂无当前账号可见的进展反馈。</p>}{submitOpen && <FeedbackDialog title="提交本人任务进展" onClose={() => setSubmitOpen(false)} onSubmit={async (form, key) => { await submitTaskFeedback(project.id, task, { proposed_status: String(form.get('status')) as TaskFeedback['proposed_status'], note: String(form.get('note')) }, key); setSubmitOpen(false); setRetry((value) => value + 1) }}><p className="ui-hint">反馈先供项目管理者审核，采用后的结果说明对组织可见。</p><label htmlFor="feedback-status">建议任务状态</label><NativeSelect id="feedback-status" name="status" defaultValue="in_progress">{Object.entries(labels).map(([status, label]) => <option key={status} value={status}>{label}</option>)}</NativeSelect><label htmlFor="feedback-note">进展、完成结果或阻塞原因</label><Textarea id="feedback-note" name="note" required rows={4} maxLength={4000} /></FeedbackDialog>}{review && <FeedbackDialog title={review.action === 'accept' ? '审核任务进展' : '拒绝进展建议'} onClose={() => setReview(null)} onSubmit={async (form) => { await reviewTaskFeedback(project.id, review.item, review.action, String(form.get('note'))); setReview(null); setRetry((value) => value + 1); onChanged() }}><p>任务 v{review.item.task_version}：{labels[review.item.proposed_status]}建议</p><p>{review.item.note}</p>{review.action === 'accept' && <p>采用会更新组织共享的正式任务状态和结果，旧版本或不允许的状态转换会保留原任务。</p>}<label htmlFor="review-feedback-note">审核说明</label><Textarea id="review-feedback-note" name="note" required rows={3} maxLength={2000} /></FeedbackDialog>}</section>
}

function FeedbackDialog({ title, children, onClose, onSubmit }: { title: string; children: React.ReactNode; onClose: () => void; onSubmit: (data: FormData, key: string) => Promise<void> }) {
  const [pending, setPending] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [closing, setClosing] = useState(false)
  const [error, setError] = useState('')
  const key = useRef(crypto.randomUUID())
  useEffect(() => {
    const protect = (event: Event) => { if (dirty || pending) { event.preventDefault(); setClosing(true) } }
    const unload = (event: BeforeUnloadEvent) => { if (dirty || pending) event.preventDefault() }
    window.addEventListener('omni-grid:before-route-change', protect)
    window.addEventListener('beforeunload', unload)
    return () => { window.removeEventListener('omni-grid:before-route-change', protect); window.removeEventListener('beforeunload', unload) }
  }, [dirty, pending])
  function close() { if (dirty) setClosing(true); else onClose() }
  async function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); if (pending) return; const data = new FormData(event.currentTarget); setPending(true); setError(''); try { await onSubmit(data, key.current) } catch (cause) { setError(cause instanceof Error ? cause.message : '反馈保存失败。'); setPending(false) } }
  return <ProjectDialog title={title} onClose={close} busy={pending}><form className="project-form" onChange={() => setDirty(true)} onSubmit={(event) => void submit(event)}><fieldset className="project-task-form-fields" disabled={pending}>{children}</fieldset>{error && <p role="alert" className="form-notice error">{error}</p>}{closing && <div role="alert"><p>反馈有未保存修改。是否放弃？</p><Button variant="outline" type="button" disabled={pending} onClick={() => setClosing(false)}>继续填写</Button><Button variant="destructive" type="button" disabled={pending} onClick={onClose}>放弃反馈</Button></div>}<div className="dialog-actions"><Button variant="outline" type="button" disabled={pending} onClick={close}>取消</Button><Button type="submit" variant="default" disabled={pending}>{pending ? '正在保存…' : '确认提交'}</Button></div></form></ProjectDialog>
}
