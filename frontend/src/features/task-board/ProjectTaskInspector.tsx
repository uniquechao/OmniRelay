import { useProjectRevision } from './project-state'
import { useCallback, useEffect, useEffectEvent, useRef, useState, type FormEvent, type CSSProperties } from 'react'
import { Dialog, Modal, ModalOverlay } from 'react-aria-components'
import { ApiError } from './api'
import { Button } from './controls'
import { Input } from './controls'
import { Textarea } from './controls'
import { NativeSelect } from './controls'
import { Columns3, ChevronRight, PanelRightClose, Send } from 'lucide-react'
import { TaskParticipantsField } from './TaskParticipantsField'
import { TaskOwnerField } from './TaskOwnerField'
import { TaskDateField } from './TaskDateField'
import { readTaskEditValues, taskEditSnapshot, taskEditValues } from './task-edit-values'
import { TaskLabelField } from './TaskLabelField'
import { ProjectTaskActivity } from './ProjectTaskActivity'
import { ProjectTaskFeedback } from './ProjectTaskFeedback'
import { ProjectTaskEvidence } from './ProjectTaskEvidence'
import { taskDate, taskDateInstant } from './task-dates'
import {
  commentOnTask,
  createProjectTask,
  deleteProjectTask,
  getProjectBoard,
  getProjectTask,
  updateProjectTask,
  type ProjectDetail,
  type ProjectTask,
  type TaskTag,
  type TaskFields,
  type TaskStatus,
  type BoardColumn,
} from './api'
import './project-task-board.css'

const statuses: Record<TaskStatus, string> = {
  todo: '待开始',
  in_progress: '进行中',
  blocked: '受阻',
  done: '已完成',
  cancelled: '已取消',
}
export type TaskEditIntent = { columnId: string }

export function ProjectTaskInspector({
  project,
  taskId,
  canManage,
  onClose,
  onChanged,
  mode = 'edit',
  initialColumnId,
  onCreated,
}: {
  project: ProjectDetail
  taskId: string
  canManage: boolean
  onClose: () => void
  onChanged: () => void
  mode?: 'create' | 'edit'
  initialColumnId?: string
  onCreated?: (task: ProjectTask) => void
}) {
  const isNew = mode === 'create'
  const timezone = project.business_timezone || 'UTC'
  const stateRevision = useProjectRevision('tasks')
  const verifiedRevision = useRef(stateRevision)
  const draftDirty = useRef(false)
  const visibleVersion = useRef<number | null>(null)
  const [formEpoch, setFormEpoch] = useState(0)
  const [task, setTask] = useState<ProjectTask | null>(() => isNew ? { id: '', project_id: project.id, title: '', description: '', owner_member_id: canManage ? null : project.current_member_id, participant_ids: [], priority: null, due_at: null, start_at: null, tag_ids: [], acceptance: '', status: 'todo', column_id: initialColumnId, blocked_from: null, blocked_reason: '', completion_note: '', version: 0, created_by: '', updated_by: '', created_at: '', updated_at: '', overdue: false } : null)
  const [selectedOwner, setSelectedOwner] = useState<string | null>(isNew && !canManage ? project.current_member_id : null)
  const [selectedTags, setSelectedTags] = useState<string[]>([])
  const [selectedColumn, setSelectedColumn] = useState(initialColumnId ?? '')
  const [boardColumns, setBoardColumns] = useState<BoardColumn[]>([])
  const appliedIntent = useRef(false)
  const loadedEditTask = useRef<ProjectTask | null>(null)
  const loadedEditColumns = useRef<BoardColumn[] | null>(null)
  const [draftStatus, setDraftStatus] = useState<TaskStatus>('todo')
  const [columnNames, setColumnNames] = useState<Record<string, string>>({})
  const [tags, setTags] = useState<TaskTag[] | null>(null)
  const [error, setError] = useState('')
  const [draftValues, setDraftValues] = useState(() => taskEditValues(task, project.business_timezone))
  const [ownerSearch, setOwnerSearch] = useState('')
  const [comment, setComment] = useState('')
  const [feedbackOpen, setFeedbackOpen] = useState(false)
  const [deleteVersion, setDeleteVersion] = useState<number | null>(null)
  const [deleteReason, setDeleteReason] = useState('')
  const [closing, setClosing] = useState(false)
  const [pending, setPending] = useState(false)
  const [denied, setDenied] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [latest, setLatest] = useState<ProjectTask | null>(null)
  const [revision, setRevision] = useState(0)
  const [retry, setRetry] = useState(0)
  const [notice, setNotice] = useState('')
  const form = useRef<HTMLFormElement>(null)
  const ownerInput = useRef<HTMLInputElement>(null)
  const commentInput = useRef<HTMLTextAreaElement>(null)
  const keepEditing = useRef<HTMLButtonElement>(null)
  const closeButton = useRef<HTMLButtonElement>(null)
  const returnFocus = useRef<HTMLElement | null>(null)
  const active = useRef(true)
  const busy = useRef(false)
  const attempts = useRef<Record<string, { signature: string; key: string }>>(
    {},
  )
  const historicalPeople = [task?.owner_member_id, ...(task?.participant_ids ?? [])]
    .filter((id): id is string => !!id && !!task?.relation_names?.[id] && !project.members.some(member => member.id === id))
    .map(id => ({id, display_name: task!.relation_names![id], active: false}))
  const taskPeople = [...project.members, ...historicalPeople]
  const revoked = useRef(false)
  const failure = useCallback((cause: unknown) => {
    if (!active.current) return
    if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
      revoked.current = true
      setDenied(true)
      setTask(null)
      setComment('')
      setLatest(null)
    }
    if (cause instanceof ApiError && cause.status === 409) {
      setConflict(true)
      setLatest(null)
    }
    setError(cause instanceof Error ? cause.message : '操作失败，请重试。')
  }, [])
  const acceptPristineTask = useCallback((next: ProjectTask) => {
    if (!active.current || revoked.current) return
    if (draftDirty.current){if(visibleVersion.current!==next.version){setLatest(next);setConflict(true)}return}
    if (visibleVersion.current !== next.version) {
      const focused = document.activeElement
      const name = focused && form.current?.contains(focused) ? focused.getAttribute('name') : null
      const choice = focused instanceof HTMLInputElement && ['checkbox', 'radio'].includes(focused.type) ? focused.value : null
      const start = focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement ? focused.selectionStart : null
      const end = focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement ? focused.selectionEnd : null
      setFormEpoch(value => value + 1)
      if (name) requestAnimationFrame(() => {
        if (!active.current || draftDirty.current) return
        const target = Array.from(form.current?.elements ?? []).find(element => element.getAttribute('name') === name && (choice === null || element.getAttribute('value') === choice))
        if (target instanceof HTMLElement) target.focus({ preventScroll: true })
        if ((target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) && start !== null && end !== null) target.setSelectionRange(Math.min(start, target.value.length), Math.min(end, target.value.length))
      })
    }
    setDraftValues(taskEditValues(next, project.business_timezone)); setOwnerSearch('')
    setTask(next); setDraftStatus(next.status); setSelectedOwner(next.owner_member_id); setSelectedTags(next.tag_ids ?? []); setSelectedColumn(next.status === 'cancelled' ? 'cancelled' : next.column_id ?? next.status)
  }, [project.business_timezone])
  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
    }
  }, [])
  const initialValues = useEffectEvent((result: ProjectTask) => taskEditValues(result, timezone))
  const applyInitialIntent = useCallback(() => {
    if (isNew || !initialColumnId || !loadedEditTask.current || loadedEditColumns.current === null || appliedIntent.current) return
    appliedIntent.current = true
    const column = loadedEditColumns.current.find(item => item.id === initialColumnId)
    setSelectedColumn(initialColumnId)
    if (column) setDraftStatus(column.stage)
    else setError('目标状态列已变更，请重新选择当前状态列。')
    if (column?.stage === 'blocked') requestAnimationFrame(() => {
      if (active.current && !revoked.current) form.current?.querySelector<HTMLTextAreaElement>('[name=blocked_reason]')?.focus({ preventScroll: true })
    })
  }, [isNew, initialColumnId])
  useEffect(() => {
    const controller = new AbortController()
    if (!isNew) getProjectTask(project.id, taskId, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted && !revoked.current) {
          setTask(result)
          setDraftValues(initialValues(result)); setOwnerSearch('')
          setDraftStatus(result.status)
          setSelectedOwner(result.owner_member_id)
          setSelectedTags(result.tag_ids ?? [])
          setSelectedColumn(result.status === 'cancelled' ? 'cancelled' : result.column_id ?? result.status)
          setError('')
          setDenied(false)
          loadedEditTask.current = result
          applyInitialIntent()
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) failure(cause)
      })
    getProjectBoard(project.id, undefined, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) {
          setTags(result.tags ?? [])
          setBoardColumns(result.columns ?? [])
          loadedEditColumns.current = result.columns ?? []
          applyInitialIntent()
          if (isNew) {
            const column = result.columns?.find(item => item.id === (initialColumnId || result.default_column_id))
            if (column) { setTask(current => current ? { ...current, column_id: column.id, status: column.stage } : current); setSelectedColumn(column.id); setDraftStatus(column.stage) }
          }
          setColumnNames(
            Object.fromEntries(
              (result.columns ?? []).map((column) => [column.id, column.name]),
            ),
          )
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) failure(cause)
      })
    return () => controller.abort()
  }, [project.id, taskId, retry, failure, isNew, initialColumnId, applyInitialIntent])
  const dirty = Boolean(task && !denied && taskEditSnapshot(draftValues, selectedOwner, selectedTags, selectedColumn, draftStatus, ownerSearch) !== taskEditSnapshot(taskEditValues(task, project.business_timezone), task.owner_member_id, task.tag_ids ?? [], task.status === 'cancelled' ? 'cancelled' : task.column_id ?? task.status, task.status))
  useEffect(() => { draftDirty.current = dirty || Boolean(comment.trim()); visibleVersion.current = task?.version ?? null }, [dirty, comment, task])
  // 编辑时只核验读取权，保留人工并发基线；未编辑时更新实际任务事实。
  useEffect(() => {
    if (isNew) return
    const controller = new AbortController()
    const check = () => {
      void getProjectTask(project.id, taskId, controller.signal).then(next => {
        if (!controller.signal.aborted) acceptPristineTask(next)
      }).catch(
        (cause) => {
          if (!controller.signal.aborted) failure(cause)
        },
      )
    }
    if (stateRevision !== undefined && verifiedRevision.current !== stateRevision) check()
    if (stateRevision !== undefined && verifiedRevision.current !== stateRevision) {
      void getProjectBoard(project.id, undefined, controller.signal).then(next => {
        if (controller.signal.aborted) return
        setTags(next.tags ?? [])
        setBoardColumns(next.columns ?? [])
        setColumnNames(Object.fromEntries((next.columns ?? []).map(column => [column.id, column.name])))
      }).catch(cause => { if (!controller.signal.aborted) failure(cause) })
    }
    verifiedRevision.current = stateRevision
    const timer = stateRevision === undefined ? setInterval(check, 5000) : undefined
    return () => {
      clearInterval(timer)
      controller.abort()
    }
  }, [project.id, taskId, failure, stateRevision, acceptPristineTask, isNew])
  const unsaved = dirty || Boolean(comment.trim())
  const requestClose = useCallback(() => {
    if (!closing) returnFocus.current = document.activeElement instanceof HTMLElement && document.activeElement?.closest('.task-inspector-dialog') ? document.activeElement : closeButton.current
    setClosing(true)
    keepEditing.current?.focus({ preventScroll: true })
  }, [closing])
  useEffect(() => {
    const guard = (event: Event) => {
      if (unsaved || pending) {
        event.preventDefault()
        requestClose()
      }
    }
    const unload = (event: BeforeUnloadEvent) => {
      if (unsaved || pending) event.preventDefault()
    }
    window.addEventListener('omni-grid:before-route-change', guard)
    window.addEventListener('beforeunload', unload)
    return () => {
      window.removeEventListener('omni-grid:before-route-change', guard)
      window.removeEventListener('beforeunload', unload)
    }
  }, [unsaved, pending, requestClose])
  useEffect(() => {
    if (!notice) return
    const timer = setTimeout(() => setNotice(''), 6000)
    return () => clearTimeout(timer)
  }, [notice])
  useEffect(() => { if (closing) keepEditing.current?.focus({ preventScroll: true }) }, [closing])
  function continueEditing() {
    setClosing(false)
    requestAnimationFrame(() => { if (returnFocus.current?.isConnected) returnFocus.current.focus({ preventScroll: true }) })
  }
  function close() {
    if (busy.current) return
    if (unsaved) requestClose()
    else onClose()
  }
  function key(operation: string, signature: string) {
    if (attempts.current[operation]?.signature !== signature)
      attempts.current[operation] = { signature, key: crypto.randomUUID() }
    return attempts.current[operation].key
  }
  const requiresChangeReason = !isNew && task && draftStatus !== task.status &&
    (draftStatus === 'cancelled' || task.status === 'done' || task.status === 'cancelled')
  const reasonPrompt = draftStatus === 'cancelled' ? '请填写取消原因。' : '请填写变更原因。'
  async function remove(event: FormEvent) {
    event.preventDefault()
    const reason = deleteReason.trim()
    if (!task || deleteVersion === null || !reason || busy.current || !canManage || conflict) return
    busy.current = true
    setPending(true)
    setError('')
    try {
      await deleteProjectTask(project.id, task.id, deleteVersion, reason, key('delete', JSON.stringify([task.id, deleteVersion, reason])))
      if (active.current && !revoked.current) { onChanged(); onClose() }
    } catch (cause) {
      if (cause instanceof ApiError && [401,403,404,409].includes(cause.status)) setDeleteVersion(null)
      failure(cause)
    } finally {
      busy.current = false
      if (active.current) setPending(false)
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault()
    if (!task || busy.current || conflict) return
    const data = new FormData(form.current!)
    if (ownerSearch) { setError('请从搜索结果中选择负责人。'); ownerInput.current?.focus(); return }
    if (!String(data.get('title') ?? '').trim()) { setError('请输入任务名称。'); form.current?.querySelector<HTMLInputElement>('[name=title]')?.focus(); return }
    if (draftStatus === 'blocked' && !String(data.get('blocked_reason') ?? '').trim()) { setError('请填写受阻原因。'); form.current?.querySelector<HTMLTextAreaElement>('[name=blocked_reason]')?.focus(); return }
    if (requiresChangeReason && !String(data.get('change_reason') ?? '').trim()) {
      setError(reasonPrompt)
      form.current?.querySelector<HTMLTextAreaElement>('[name=change_reason]')?.focus()
      return
    }
    if (draftValues.start && draftValues.due && draftValues.start > draftValues.due) { setError('截止日期不能早于开始日期。'); form.current?.querySelector<HTMLInputElement>('[name=due]')?.focus(); return }
    let fields: TaskFields
    try {
      if (boardColumns.length && draftStatus !== 'cancelled' && !boardColumns.some(column => column.id === selectedColumn)) {
        throw new Error('状态列已变更，请重新选择当前状态列。')
      }
      const date = (
        name: string,
        original: string | null | undefined,
        end = false,
      ) => {
        const entered = String(data.get(name) ?? '')
        return entered === taskDate(original, timezone)
          ? (original ?? null)
          : entered
            ? taskDateInstant(entered, timezone, end)
            : null
      }
      fields = canManage
        ? {
            title: String(data.get('title')).trim(),
            description: String(data.get('description') ?? ''),
            owner_member_id: String(data.get('owner') ?? '') || null,
            participant_ids: draftValues.participant_ids,
            priority: (String(data.get('priority') ?? '') || null) as TaskFields['priority'],
            acceptance: String(data.get('acceptance') ?? ''),
            start_at: date('start', task.start_at),
            due_at: date('due', task.due_at, true),
            tag_ids:
              tags === null ? task.tag_ids : selectedTags,
          }
        : {
            title: String(data.get('title') ?? task.title).trim(),
            description: String(data.get('description') ?? task.description),
            start_at:task.start_at,tag_ids:task.tag_ids,
            owner_member_id: task.owner_member_id,
            participant_ids: task.participant_ids,
            priority: task.priority,
            acceptance: task.acceptance,
            due_at: task.due_at,
          }
      const command = {
        ...fields,
        status: draftStatus,
        column_id: boardColumns.some(column => column.id === selectedColumn) ? selectedColumn : undefined,
        blocked_reason: draftStatus === 'blocked' ? String(data.get('blocked_reason') ?? '').trim() : '',
        completion_note: String(data.get('completion_note') ?? ''),
        change_reason: requiresChangeReason ? String(data.get('change_reason') ?? '').trim() : '',
      }
      busy.current = true
      setPending(true)
      setError('')
      const saved = isNew
        ? await createProjectTask(project.id, { ...fields, column_id: command.column_id, blocked_reason: command.blocked_reason }, key('create', JSON.stringify(['create', fields, command.column_id, command.blocked_reason])))
        : await updateProjectTask(project.id, task, { ...command, request_key: key('save', JSON.stringify(['save', task.version, command])) })
      if (active.current && !revoked.current) {
        setTask(saved)
        setDraftValues(taskEditValues(saved, project.business_timezone)); setOwnerSearch('')
        setSelectedOwner(saved.owner_member_id); setSelectedTags(saved.tag_ids ?? []); setDraftStatus(saved.status); setSelectedColumn(saved.status === 'cancelled' ? 'cancelled' : saved.column_id ?? saved.status)
        setFormEpoch(value => value + 1)
          setRevision((n) => n + 1)
        setNotice(isNew ? '任务已创建。' : '任务已保存。')
        onChanged()
        if (isNew) { if (onCreated) onCreated(saved); else onClose() }
        else if (comment.trim()) requestAnimationFrame(() => commentInput.current?.focus({ preventScroll: true }))
        else onClose()
      }
    } catch (cause) {
      failure(cause)
    } finally {
      busy.current = false
      if (active.current) setPending(false)
    }
  }
  async function send() {
    if (!task || busy.current || !comment.trim() || conflict) return
    busy.current = true
    setPending(true)
    setError('')
    try {
      const event = await commentOnTask(
        project.id,
        task.id,
        task.version,
        key('comment', JSON.stringify(['comment', task.version, comment])),
        comment,
      )
      if (active.current && !revoked.current) {
        delete attempts.current.comment
        setComment('')
        setRevision((n) => n + 1)
        setNotice('评论已保存。')
        const id = project.current_member_id
        if (id) setTask(current => {
          if (!current || current.interaction_participants?.some(person => person.id === id)) return current
          const actor = project.members.find(person => person.id === id) ?? {id, display_name: event.actor_name || id}
          return {...current, interaction_participants:[...(current.interaction_participants ?? []),actor]}
        })
      }
    } catch (cause) {
      failure(cause)
    } finally {
      busy.current = false
      if (active.current) setPending(false)
    }
  }
  const own = Boolean(
    task &&
    project.actions?.includes('update_own_task') &&
    project.current_member_id &&
    [task.owner_member_id, ...task.participant_ids].includes(
      project.current_member_id,
    ) &&
    !['done', 'cancelled'].includes(task.status),
  )
  return (
        <ModalOverlay
          isOpen
          isDismissable
          isKeyboardDismissDisabled={pending || closing}
          onOpenChange={(open) => {
            if (!open) close()
          }}
          className="task-inspector-layer"
        >
          <Modal className="task-inspector-panel">
            <Dialog aria-label={isNew ? '新建任务' : '任务详情'} className="task-inspector-dialog">
              <header className="task-inspector-top">
                <span className="task-inspector-path"><Columns3 size={15} aria-hidden /><span className="task-path-project">{project.name}</span><ChevronRight size={14} aria-hidden />{!isNew && task && <span className="task-path-code">T-{String(task.display_number ?? '').padStart(3,'0')}</span>}<span className="task-path-title">{isNew ? '新建任务' : task?.title ?? '任务详情'}</span></span>
                <div>
                  {!isNew && task && canManage && !denied && <Button variant="outline" aria-haspopup="dialog" disabled={pending || conflict || unsaved}
                    title={unsaved ? '请先保存或放弃更改' : '删除任务'} onClick={() => {setDeleteReason(''); setDeleteVersion(task.version); setError('')}}>删除任务</Button>}
                  <Button
                    form="task-inspector-form"
                    type="submit"
                    disabled={
                      !task ||
                      (isNew && (tags === null || !draftValues.title.trim())) ||
                      pending ||
                      !dirty ||
                      conflict ||
                      (!canManage && !own && !isNew)
                    }
                  >
                    {isNew ? '创建任务' : '保存任务'}
                  </Button>
                  <Button
                    ref={closeButton}
                    variant="ghost"
                    onClick={close}
                    disabled={pending}
                    aria-label="收起任务详情"
                  >
                    <PanelRightClose size={16} aria-hidden />
                  </Button>
                </div>
              </header>
              {deleteVersion !== null && task && !denied && <ModalOverlay isOpen isDismissable={!pending} isKeyboardDismissDisabled={pending}
                onOpenChange={open => { if (!open && !busy.current) setDeleteVersion(null) }} className="task-delete-overlay">
                <Modal className="task-delete-modal"><Dialog aria-label="删除任务">
                  <form onSubmit={remove}><h3>删除「{task.title}」？</h3><p>删除后任务将从看板和搜索中移除，操作历史仍会保留。</p>
                    <label>删除原因<Textarea autoFocus required maxLength={4000} value={deleteReason} onChange={event => setDeleteReason(event.target.value)} disabled={pending}/></label>
                    {error && <p role="alert">{error}</p>}
                    <div className="dialog-actions"><Button variant="outline" disabled={pending} onClick={() => setDeleteVersion(null)}>保留任务</Button>
                      <Button type="submit" disabled={!deleteReason.trim() || pending || conflict}>确认删除</Button></div>
                  </form>
                </Dialog></Modal>
              </ModalOverlay>}
              {closing && (
                <div className="task-inspector-discard" role="alert">
                  <span>有未保存的更改</span><div className="task-discard-actions">
                  <Button
                    variant="outline"
                    disabled={pending}
                    ref={keepEditing}
                    onClick={continueEditing}
                  >
                    继续编辑
                  </Button>
                  <Button variant="outline" disabled={pending} onClick={onClose}>
                    放弃更改
                  </Button></div>
                </div>
              )}
              {error && deleteVersion === null && (
                <div role="alert" className="task-inspector-notice">
                  {error}
                  {!task && !denied && (
                    <Button onClick={() => setRetry((n) => n + 1)}>
                      重新读取详情
                    </Button>
                  )}
                </div>
              )}
              {conflict && task && (
                <div className="task-inspector-notice">
                  <p>草稿已保留，核对当前版本后才能重试。</p>
                  <Button
                    disabled={pending}
                    onClick={() => {
                      setLatest(null)
                      void Promise.all([getProjectTask(project.id, task.id), getProjectBoard(project.id)])
                        .then(([result, board]) => {
                          if (active.current && !revoked.current) {
                            setLatest(result)
                            setBoardColumns(board.columns ?? [])
                            setTags(board.tags ?? [])
                            setColumnNames(Object.fromEntries((board.columns ?? []).map(column => [column.id, column.name])))
                          }
                        })
                        .catch(failure)
                    }}
                  >
                    读取最新版本
                  </Button>
                  {latest && (
                    <>
                      <div aria-label="当前任务版本">
                        <p>当前版本 v{latest.version}</p>
                        <dl>
                          {[
                            ['标题', latest.title],
                            ['正文', latest.description],
                            [
                              '负责人',
                              project.members.find(
                                (member) =>
                                  member.id === latest.owner_member_id,
                              )?.display_name ?? latest.owner_member_id,
                            ],
                            ['状态', statuses[latest.status]],
                            ['优先级', latest.priority],
                            ['开始时间', latest.start_at],
                            ['截止时间', latest.due_at],
                            [
                              '标签',
                              latest.tag_ids
                                ?.map(
                                  (id) =>
                                    tags?.find((tag) => tag.id === id)?.name ??
                                    id,
                                )
                                .join('、'),
                            ],
                            ['验收条件', latest.acceptance],
                            ['受阻原因', latest.blocked_reason],
                            ['完成结果', latest.completion_note],
                          ].map(([name, value]) => (
                            <div key={name}>
                              <dt>{name}</dt>
                              <dd>{value || '未设置'}</dd>
                            </div>
                          ))}
                        </dl>
                        <p>核对后将按当前版本提交保留的草稿，请确认各字段。</p>
                      </div>
                      <Button
                        onClick={() => {
                          setTask((current) =>
                            current
                              ? { ...current, version: latest.version, status: latest.status }
                              : current,
                          )
                          setConflict(false)
                          setLatest(null)
                          setError('')
                        }}
                      >
                        已核对，保留草稿重试
                      </Button>
                    </>
                  )}
                </div>
              )}
              {notice && (
                <p role="status" className="task-inspector-notice">
                  <span>{notice}</span>
                  <Button variant="ghost" onClick={() => setNotice('')}>
                    关闭提示
                  </Button>
                </p>
              )}
              {!task && !error && <p role="status">正在读取任务详情…</p>}
              {task && (
                <div className="task-inspector-body">
                  <form
                    key={`pristine-task-form-${formEpoch}`}
                    id="task-inspector-form"
                    ref={form}
                    className="task-inspector-fields"
                    onSubmit={save}
                    onChange={event => { if (event.currentTarget.contains(event.target as Node)) setDraftValues(readTaskEditValues(event.currentTarget, draftValues)) }}
                  >
                    <section className="task-inspector-copy">
                      <h2 className="sr-only">{isNew ? '新建任务' : task.title}</h2>
                      <label><span className="sr-only">任务标题</span><Input aria-label="任务标题" name="title" defaultValue={task.title} placeholder="输入任务名称" required maxLength={200} readOnly={(!canManage && !own && !isNew) || pending} /></label>
                      <label><span className="sr-only">任务正文</span><Textarea aria-label="任务正文" name="description" defaultValue={task.description} placeholder="添加任务说明…" rows={2} maxLength={10000} readOnly={(!canManage && !own && !isNew) || pending} /></label>
                      <label hidden={draftStatus !== 'blocked'} className="task-blocker-field">受阻原因<Textarea aria-label="受阻原因" name="blocked_reason" disabled={draftStatus !== 'blocked'} required={draftStatus === 'blocked'} defaultValue={task.blocked_reason} rows={2} maxLength={4000} readOnly={(!canManage && !own && !isNew) || pending} /></label>
                      {!isNew && <label hidden={draftStatus !== 'done' && !task.completion_note}>完成结果<Textarea aria-label="完成结果" name="completion_note" defaultValue={task.completion_note} rows={2} maxLength={4000} readOnly={(!canManage && !own && !isNew) || pending} /></label>}
                      {requiresChangeReason && canManage && <label>{draftStatus === 'cancelled' ? '取消原因（必填）' : '变更原因（必填）'}<Textarea aria-label={draftStatus === 'cancelled' ? '取消原因' : '变更原因'} aria-required="true" name="change_reason" maxLength={4000} rows={2} readOnly={pending} onChange={() => {if (error === reasonPrompt) setError('')}} /></label>}
                      <details className="task-inspector-extra"><summary>验收条件</summary><label>验收条件<Textarea aria-label="验收条件" name="acceptance" defaultValue={task.acceptance} maxLength={4000} rows={2} readOnly={!canManage || pending} /></label></details>
                    </section>
                    <aside className="task-inspector-sidebar" aria-label="任务属性">
                      <section className="task-side-section"><h3>属性</h3>
                        <label className="task-property"><span>状态</span><span className="task-status-control" data-stage={draftStatus} style={{ '--task-status-dot': boardColumns.find(column => column.id === selectedColumn)?.color || ({ todo: '#7b8495', in_progress: '#1746e8', blocked: '#b66b16', done: '#528a14', cancelled: '#7b8495' })[draftStatus] } as CSSProperties}><NativeSelect aria-label="任务状态" name="column" value={selectedColumn || draftStatus} disabled={(!canManage && !own && !isNew) || pending} onChange={event => { setSelectedColumn(event.target.value); setDraftStatus(boardColumns.find(column => column.id === event.target.value)?.stage ?? event.target.value as TaskStatus) }}>{boardColumns.length > 0 && selectedColumn && selectedColumn !== 'cancelled' && !boardColumns.some(column => column.id === selectedColumn) && <option value={selectedColumn} disabled>原状态列已移除，请选择当前状态列</option>}{boardColumns.length ? boardColumns.map(column => <option key={column.id} value={column.id}>{column.name}</option>) : Object.entries(statuses).filter(([status]) => status !== 'cancelled' || !isNew && (canManage || task.status === 'cancelled')).map(([status,label]) => <option key={status} value={status}>{label}</option>)}{boardColumns.length > 0 && !isNew && (canManage || task.status === 'cancelled') && <option value="cancelled">已取消</option>}</NativeSelect></span></label>
                        <label className="task-property"><span>优先级</span><NativeSelect aria-label="优先级" name="priority" defaultValue={task.priority ?? ''} disabled={!canManage || pending}><option value="">未设置</option>{Object.entries({P0:'紧急',P1:'高',P2:'中',P3:'低'}).map(([value,label]) => <option value={value} key={value}>{label}</option>)}</NativeSelect></label>
                        <div className="task-property task-owner-property"><span aria-hidden>主负责人</span><input type="hidden" name="owner" value={selectedOwner ?? ''} /><TaskOwnerField members={taskPeople.filter(member => member.active !== false || member.id === task.owner_member_id)} value={selectedOwner} inputRef={ownerInput} onChange={value => { setSelectedOwner(value); setError(current => current === '请从搜索结果中选择负责人。' ? '' : current) }} onQueryChange={setOwnerSearch} disabled={!canManage || pending} /></div>
                        <TaskParticipantsField members={taskPeople} value={draftValues.participant_ids} interactions={task.interaction_participants} disabled={!canManage || pending} onChange={ids => setDraftValues(current => ({ ...current, participant_ids: ids }))} />
                        <TaskDateField timezone={timezone} label="开始日期" name="start" defaultValue={taskDate(task.start_at,project.business_timezone || 'UTC')} disabled={!canManage || pending} />
                        <TaskDateField timezone={timezone} label="截止日期" name="due" defaultValue={taskDate(task.due_at,project.business_timezone || 'UTC')} disabled={!canManage || pending} />
                        <div className="task-property task-property-tags"><span>标签</span><div><div className="task-selected-tags">{selectedTags.map(id => { const tag=tags?.find(tag => tag.id === id); return <span key={id} title={tag?.type}>{tag?.name ?? '标签名称未知'}</span> })}</div><TaskLabelField projectId={project.id} tags={tags ?? []} value={selectedTags} onChange={setSelectedTags} onTags={setTags} disabled={!canManage || pending || tags === null} canCreate={project.actions.includes('configure_board')} /></div></div>
                      </section>
                      {!isNew && <section className="task-side-section"><h3>详情</h3><dl className="task-info-list"><dt>创建者</dt><dd>{task.created_by_name || task.created_by || '未知'}</dd><dt>创建于</dt><dd><time dateTime={task.created_at}>{task.created_at && new Date(task.created_at).toLocaleString('zh-CN',{timeZone:project.business_timezone || 'UTC',hour12:false})}</time></dd><dt>更新于</dt><dd><time dateTime={task.updated_at}>{task.updated_at && new Date(task.updated_at).toLocaleString('zh-CN',{timeZone:project.business_timezone || 'UTC',hour12:false})}</time></dd></dl><details className="task-version-info"><summary>版本与更新人</summary><p>版本 v{task.version}</p><p>更新人：{task.updated_by_name || task.updated_by || '未知'}</p><p>业务时区：{project.business_timezone || 'UTC（业务时区未设置）'}</p></details><ProjectTaskEvidence projectId={project.id} taskId={task.id} /></section>}
                    </aside>
                  </form>
                  {!isNew && (project.actions?.includes('comment') ?? canManage) && (
                    <div className="task-inspector-comment">
                      <label>
                        <span className="sr-only">发表评论</span>
                        <Textarea
                          ref={commentInput}
                          aria-label="发表评论"
                          placeholder="留下评论…"
                          rows={1}
                          value={comment}
                          maxLength={4000}
                          disabled={pending}
                          onChange={(event) => setComment(event.target.value)}
                        />
                      </label>
                      <Button
                        disabled={!comment.trim() || pending || conflict}
                        onClick={() => void send()}
                        aria-label="提交评论"
                      >
                        <Send size={15} aria-hidden />
                      </Button>
                    </div>
                  )}
                  {!isNew && <ProjectTaskActivity
                    key={revision}
                    project={project}
                    taskId={task.id}
                    revision={revision}
                    tags={tags ?? []}
                    columnNames={columnNames}
                    onError={failure}
                  />}
                  {!isNew && <details
                    className="task-inspector-feedback"
                    onToggle={(event) => {
                      if (event.target === event.currentTarget)
                        setFeedbackOpen(event.currentTarget.open)
                    }}
                  >
                    <summary>进展反馈与审核</summary>
                    {feedbackOpen && (
                      <ProjectTaskFeedback
                        project={project}
                        task={task}
                        canManage={canManage}
                        onChanged={() => {
                          onChanged()
                          setRevision((n) => n + 1)
                          if (dirty) {
                            setConflict(true)
                            setLatest(null)
                          } else
                            void getProjectTask(project.id, task.id)
                              .then((result) => {
                                if (active.current && !revoked.current) {
                                  acceptPristineTask(result)
                                }
                              })
                              .catch(failure)
                        }}
                      />
                    )}
                  </details>}
                </div>
              )}
            </Dialog>
          </Modal>
        </ModalOverlay>
  )
}
