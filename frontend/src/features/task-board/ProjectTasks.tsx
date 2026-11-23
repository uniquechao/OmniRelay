import { Textarea } from './controls'
import { NativeSelect } from './controls'
import { Input } from './controls'
import { Button } from './controls'
import { useEffect, useRef, useState, type FormEvent } from 'react'

import { taskDate, taskDateInstant } from './task-dates'
import { ProjectDialog } from './ProjectDialog'
import { createProjectTask, getProjectBoard, updateProjectTask, type ProjectDetail, type ProjectTask, type TaskFields, type TaskStatus, type TaskTag } from './api'

const labels: Record<TaskStatus, string> = { todo: '待开始', in_progress: '进行中', blocked: '受阻', done: '已完成', cancelled: '已取消' }
export function ProjectTaskEditor({ project, task, onClose, onSaved, initial, title, onCreate, requireShare = false, ownOnly = false }: { project: ProjectDetail; task: ProjectTask | null; onClose: () => void; onSaved: (task: ProjectTask) => void; initial?: Partial<TaskFields>; title?: string; onCreate?: (fields: TaskFields, key: string) => Promise<ProjectTask>; requireShare?: boolean; ownOnly?: boolean }) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [dirty, setDirty] = useState(false)
  const [closing, setClosing] = useState(false)
  const key = useRef(crypto.randomUUID())
  const [tags, setTags] = useState<TaskTag[] | null>(null)
  const timezone = project.business_timezone || 'UTC'
  useEffect(() => { const controller = new AbortController(); getProjectBoard(project.id, undefined, controller.signal).then(board => { if (!controller.signal.aborted) setTags(board.tags ?? []) }).catch(() => {}); return () => controller.abort() }, [project.id])
  useEffect(() => {
    const protect = (event: Event) => { if (dirty) { event.preventDefault(); setClosing(true) } }
    const unload = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault() }
    window.addEventListener('omni-grid:before-route-change', protect); window.addEventListener('beforeunload', unload)
    return () => { window.removeEventListener('omni-grid:before-route-change', protect); window.removeEventListener('beforeunload', unload) }
  }, [dirty])
  function close() { if (dirty) setClosing(true); else onClose() }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (pending) return
    const data = new FormData(event.currentTarget)
    const deadline = String(data.get('deadline') ?? '')
    const start = String(data.get('start') ?? '')
    let fields: TaskFields
    try {
      const originalDue = task?.due_at ?? initial?.due_at ?? null
      const originalStart = task?.start_at ?? initial?.start_at ?? null
      fields = ownOnly && task ? { title: task.title, description: task.description, owner_member_id: task.owner_member_id, participant_ids: task.participant_ids, priority: task.priority, due_at: task.due_at, acceptance: task.acceptance } : { title: String(data.get('title') ?? '').trim(), description: String(data.get('description') ?? ''), owner_member_id: String(data.get('owner') ?? '') || null, participant_ids: data.getAll('participants').map(String), priority: (String(data.get('priority') ?? '') || null) as TaskFields['priority'], due_at: deadline ? deadline === taskDate(originalDue, timezone) ? originalDue : taskDateInstant(deadline, timezone, true) : null, start_at: start ? start === taskDate(originalStart, timezone) ? originalStart : taskDateInstant(start, timezone) : null, tag_ids: tags ? data.getAll('tags').map(String) : task?.tag_ids ?? initial?.tag_ids, acceptance: String(data.get('acceptance') ?? '').trim() }
    } catch (cause) { setError(cause instanceof Error ? cause.message : '日期无效。'); return }
    if (requireShare && !data.get('share_ack')) { setError('请确认任务结论可作为项目资料对组织可见。'); return }
    if (requireShare && !deadline && !data.get('no_deadline_ack')) { setError('候选期限未明确，请填写期限或明确确认暂不设置期限。'); return }
    if (!fields.title) { setError('请填写任务标题。'); return }
    setPending(true); setError('')
    try {
      const saved = task ? await updateProjectTask(project.id, task, { ...fields, status: String(data.get('status')) as TaskStatus, blocked_reason: String(data.get('blocked_reason') ?? ''), completion_note: String(data.get('completion_note') ?? ''), change_reason: String(data.get('change_reason') ?? '') }) : onCreate ? await onCreate(fields, key.current) : await createProjectTask(project.id, fields, key.current)
      onSaved(saved)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '任务保存失败，请重试。'); setPending(false) }
  }
  return <ProjectDialog title={title ?? (task ? '编辑任务与状态' : '新建任务')} onClose={close} busy={pending}>
    <form className="project-form" onChange={() => setDirty(true)} onSubmit={(event) => void submit(event)}>
      <fieldset disabled={pending} className="project-task-form-fields">
        <fieldset disabled={ownOnly} className="project-task-form-fields">
        <label htmlFor="task-title">任务标题</label><Input id="task-title" name="title" defaultValue={task?.title ?? initial?.title ?? ''} required maxLength={200} />
        <label htmlFor="task-owner">任务主责人</label><NativeSelect id="task-owner" name="owner" defaultValue={task ? task.owner_member_id ?? '' : initial ? initial.owner_member_id ?? '' : project.owner_member_id ?? ''}><option value="">未分配</option>{project.members.filter((member) => member.active !== false).map((member) => <option value={member.id} key={member.id}>{member.display_name}</option>)}</NativeSelect>
        <label htmlFor="task-participants">参与人</label><NativeSelect id="task-participants" name="participants" multiple defaultValue={task?.participant_ids ?? initial?.participant_ids ?? []}>{project.members.filter((member) => member.active !== false).map((member) => <option value={member.id} key={member.id}>{member.display_name}</option>)}</NativeSelect>
        <label htmlFor="task-priority">任务优先级</label><NativeSelect id="task-priority" name="priority" defaultValue={task ? task.priority ?? '' : initial?.priority === null ? '' : initial?.priority ?? 'P2'}><option value="">未设置</option>{['P0', 'P1', 'P2', 'P3'].map((priority) => <option value={priority} key={priority}>{priority}</option>)}</NativeSelect>
        <p className="field-hint">{`日期按 ${timezone} 保存，开始为当日零点，截止为日末。未编辑的原精确时刻保持不变。`}</p>
        <label htmlFor="task-start">开始日期</label><Input id="task-start" name="start" type="date" defaultValue={taskDate(task?.start_at ?? initial?.start_at ?? (task || initial ? null : new Date().toISOString()), timezone)} />
        <label htmlFor="task-deadline">截止日期</label><Input id="task-deadline" name="deadline" type="date" defaultValue={taskDate(task?.due_at ?? initial?.due_at, timezone)} />
        {requireShare && <label className="project-share-ack"><Input type="checkbox" name="no_deadline_ack" />确认暂不设置期限（仅在未填写期限时需要）</label>}
        <label htmlFor="task-tags">任务标签</label><NativeSelect id="task-tags" name="tags" multiple defaultValue={task?.tag_ids ?? initial?.tag_ids ?? []}>{tags?.map(tag => <option key={tag.id} value={tag.id}>{tag.type} · {tag.name}</option>)}</NativeSelect>
        <label htmlFor="task-acceptance">验收条件（可选）</label><Textarea id="task-acceptance" name="acceptance" defaultValue={task?.acceptance ?? initial?.acceptance ?? ''} maxLength={4000} rows={3} />
        <label htmlFor="task-description">任务说明</label><Textarea id="task-description" name="description" defaultValue={task?.description ?? initial?.description ?? ''} maxLength={10000} rows={3} />
        </fieldset>
        {task && <><label htmlFor="task-state">执行状态</label><NativeSelect id="task-state" name="status" defaultValue={task.status}>{Object.entries(labels).filter(([status]) => !ownOnly || status !== 'cancelled').map(([status, label]) => <option value={status} key={status}>{label}</option>)}</NativeSelect><label htmlFor="task-blocked">阻塞原因与需要的协助</label><Textarea id="task-blocked" name="blocked_reason" defaultValue={task.blocked_reason} maxLength={4000} rows={2} /><label htmlFor="task-completion">完成结果</label><Textarea id="task-completion" name="completion_note" defaultValue={task.completion_note} maxLength={4000} rows={2} /><label htmlFor="task-change">取消或重新打开原因</label><Textarea id="task-change" name="change_reason" maxLength={4000} rows={2} /></>}
      </fieldset>
      {requireShare && <label className="project-share-ack"><Input type="checkbox" name="share_ack" disabled={pending} />确认所填任务结论可作为项目资料对组织可见，原始依据仍按权限读取。</label>}
      {error && <p role="alert" className="form-notice error">{error}</p>}
      {closing && <div role="alert"><p>任务有未保存修改。是否放弃？</p><Button variant="outline" type="button" onClick={() => setClosing(false)}>继续编辑</Button><Button variant="outline" type="button" onClick={onClose}>放弃修改</Button></div>}
      <div className="dialog-actions"><Button variant="outline" type="button" disabled={pending} onClick={close}>取消</Button><Button type="submit" variant="default" disabled={pending}>{pending ? '正在保存…' : '保存任务'}</Button></div>
    </form>
  </ProjectDialog>
}
