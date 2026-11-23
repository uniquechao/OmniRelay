import { Textarea } from './controls'
import { NativeSelect } from './controls'
import { Input } from './controls'
import { Button } from './controls'
import { useEffect, useState, type FormEvent } from 'react'

import { taskDate, taskDateInstant } from './task-dates'
import { ProjectDialog } from './ProjectDialog'
import { getProjectBoard, type ProjectDetail, type ProjectTask, type TaskFields, type TaskTag } from './api'

export function CandidateReviewForm({project,onClose,onSaved,initial,onCreate}:{
  project:ProjectDetail; onClose:()=>void; onSaved:(task:ProjectTask)=>void;
  initial:TaskFields; onCreate:(fields:TaskFields)=>Promise<ProjectTask>;
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [dirty, setDirty] = useState(false)
  const [closing, setClosing] = useState(false)
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
      const originalDue = initial.due_at ?? null
      const originalStart = initial.start_at ?? null
      fields = { title: String(data.get('title') ?? '').trim(), description: String(data.get('description') ?? ''), owner_member_id: String(data.get('owner') ?? '') || null, participant_ids: data.getAll('participants').map(String), priority: (String(data.get('priority') ?? '') || null) as TaskFields['priority'], due_at: deadline ? deadline === taskDate(originalDue, timezone) ? originalDue : taskDateInstant(deadline, timezone, true) : null, start_at: start ? start === taskDate(originalStart, timezone) ? originalStart : taskDateInstant(start, timezone) : null, tag_ids: tags ? data.getAll('tags').map(String) : initial.tag_ids, acceptance: String(data.get('acceptance') ?? '').trim() }
    } catch (cause) { setError(cause instanceof Error ? cause.message : '日期无效。'); return }
    if (!data.get('share_ack')) { setError('请确认任务结论可作为项目资料对组织可见。'); return }
    if (!deadline && !data.get('no_deadline_ack')) { setError('候选期限未明确，请填写期限或明确确认暂不设置期限。'); return }
    if (!fields.title) { setError('请填写任务标题。'); return }
    setPending(true); setError('')
    try {
      const saved = await onCreate(fields)
      onSaved(saved)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '任务保存失败，请重试。'); setPending(false) }
  }
  return <ProjectDialog title="确认候选任务" onClose={close} busy={pending}>
    <form className="project-form" onChange={() => setDirty(true)} onSubmit={(event) => void submit(event)}>
      <fieldset disabled={pending} className="project-task-form-fields">
        <label htmlFor="task-title">任务标题</label><Input id="task-title" name="title" defaultValue={initial.title ?? ''} required maxLength={200} />
        <label htmlFor="task-owner">任务主责人</label><NativeSelect id="task-owner" name="owner" defaultValue={initial.owner_member_id ?? ''}><option value="">未分配</option>{project.members.filter((member) => member.active !== false).map((member) => <option value={member.id} key={member.id}>{member.display_name}</option>)}</NativeSelect>
        <label htmlFor="task-participants">参与人</label><NativeSelect id="task-participants" name="participants" multiple defaultValue={initial.participant_ids ?? []}>{project.members.filter((member) => member.active !== false).map((member) => <option value={member.id} key={member.id}>{member.display_name}</option>)}</NativeSelect>
        <label htmlFor="task-priority">任务优先级</label><NativeSelect id="task-priority" name="priority" defaultValue={initial.priority ?? ''}><option value="">未设置</option>{['P0', 'P1', 'P2', 'P3'].map((priority) => <option value={priority} key={priority}>{priority}</option>)}</NativeSelect>
        <p className="field-hint">{`日期按 ${timezone} 保存，开始为当日零点，截止为日末。未编辑的原精确时刻保持不变。`}</p>
        <label htmlFor="task-start">开始日期</label><Input id="task-start" name="start" type="date" defaultValue={taskDate(initial.start_at, timezone)} />
        <label htmlFor="task-deadline">截止日期</label><Input id="task-deadline" name="deadline" type="date" defaultValue={taskDate(initial.due_at, timezone)} />
        <label className="project-share-ack"><Input type="checkbox" name="no_deadline_ack" />确认暂不设置期限（仅在未填写期限时需要）</label>
        <label htmlFor="task-tags">任务标签</label><NativeSelect id="task-tags" name="tags" multiple defaultValue={initial.tag_ids ?? []}>{tags?.map(tag => <option key={tag.id} value={tag.id}>{tag.type} · {tag.name}</option>)}</NativeSelect>
        <label htmlFor="task-acceptance">验收条件（可选）</label><Textarea id="task-acceptance" name="acceptance" defaultValue={initial.acceptance ?? ''} maxLength={4000} rows={3} />
        <label htmlFor="task-description">任务说明</label><Textarea id="task-description" name="description" defaultValue={initial.description ?? ''} maxLength={10000} rows={3} />
      </fieldset>
      <label className="project-share-ack"><Input type="checkbox" name="share_ack" disabled={pending} />确认所填任务结论可作为项目资料对组织可见，原始依据仍按权限读取。</label>
      {error && <p role="alert" className="form-notice error">{error}</p>}
      {closing && <div role="alert"><p>任务有未保存修改。是否放弃？</p><Button variant="outline" type="button" onClick={() => setClosing(false)}>继续编辑</Button><Button variant="outline" type="button" onClick={onClose}>放弃修改</Button></div>}
      <div className="dialog-actions"><Button variant="outline" type="button" disabled={pending} onClick={close}>取消</Button><Button type="submit" variant="default" disabled={pending}>{pending ? '正在保存…' : '保存任务'}</Button></div>
    </form>
  </ProjectDialog>
}
