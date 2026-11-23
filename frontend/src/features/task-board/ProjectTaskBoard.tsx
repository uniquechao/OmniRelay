import { useProjectRevision } from './project-state'
import { useContext, useEffect, useRef, useState, type DragEvent, type KeyboardEvent, type ReactNode } from 'react'
import { Button } from './controls'
import { Input } from './controls'
import { NativeSelect } from './controls'
import { Textarea } from './controls'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from './controls'
import { useHashState, updateHashParams } from './hash-state'
import { AuthContext } from './project-state'
import { useViewContext } from './view-context'
import { ApiError } from './api'
import { ProjectDialog } from './ProjectDialog'
import { ProjectBoardSettings } from './ProjectBoardSettings'
import { ProjectTaskInspector, type TaskEditIntent } from './ProjectTaskInspector'
import { SearchCombobox } from './search-combobox'
import { peopleSearchText } from './people-search'
import { Popover, PopoverContent, PopoverTrigger } from './controls'
import { ArrowUpDown, Columns3, Funnel, Settings, List, ListChecks, Plus, MoreHorizontal, Search } from 'lucide-react'
import { TaskTagPicker } from './TaskTagPicker'
import { taskDate } from './task-dates'
import { boardQuery, taskTagIds } from './task-board-filters'
import {
  getProjectBoard,
  moveProjectTask,
  type ProjectBoardPage,
  type ProjectDetail,
  type ProjectTask,
  type TaskSort,
} from './api'
import { TaskBulkEdit } from './TaskBulkEdit'
import './project-task-board.css'
import './task-board-header.css'

type Destination = { task: ProjectTask; column: string; before?: string; after?: string }
export function ProjectTaskBoard({
  projectId,
  canManage,
  onOpenTask,
  project,
  refreshKey = 0,
  navigation,
}: {
  projectId: string
  canManage: boolean
  onOpenTask: (id: string, intent?: TaskEditIntent) => void
  project?: ProjectDetail
  refreshKey?: number
  navigation?: ReactNode
}) {
  const stateRevision = useProjectRevision('tasks')
  const searchInput = useRef<HTMLInputElement>(null)
  const sortTrigger = useRef<HTMLButtonElement>(null)
  const [q, setQ] = useHashState<string>('task_q', '')
  const [milestone, setMilestone] = useHashState<string>('task_milestone', '')
  const [owner, setOwner] = useHashState<string>('task_owner', '')
  const [status, setStatus] = useHashState<string>('task_status', '')
  const [legacyTag] = useHashState<string>('task_tag', '')
  const [rawTags] = useHashState<string>('task_tags', '')
  const tagIds = taskTagIds(rawTags, legacyTag)
  const [due, setDue] = useHashState<string>('task_due', '')
  const [column, setColumn] = useHashState<string>('task_column', '')
  const [priority, setPriority] = useHashState<string>('task_priority', '')
  const [overdue] = useHashState<boolean>('task_overdue', false)
  const [dueFrom, setDueFrom] = useHashState<string>('task_due_from', '')
  const [dueBefore, setDueBefore] = useHashState<string>('task_due_before', '')
  const [sort, setSort] = useHashState<TaskSort>('task_sort', 'manual', [
    'manual',
    'priority',
    'due',
    'title',
  ])
  const [board, setBoard] = useState<ProjectBoardPage | null>(null)
  const [cancelledBoard, setCancelledBoard] = useState<ProjectBoardPage | null>(null)
  const [layout,setLayout] = useHashState<'board'|'list'>('task_layout','board',['board','list'])
  const [bulk,setBulk] = useState(false)
  const [fieldsOpen,setFieldsOpen] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [loading, setLoading] = useState(false)
  const [pending, setPending] = useState(false)
  const busy = useRef(false)
  const moveAttempt = useRef<{ signature: string; key: string } | null>(null)
  const [retry, setRetry] = useState(0)
  const [settings, setSettings] = useState(false)
  const [settingsColumn, setSettingsColumn] = useState<string | undefined>(undefined)
  const [creating, setCreating] = useState<string | null>(null)
  const [reason, setReason] = useState<Destination | null>(null)
  const [drag, setDrag] = useState<ProjectTask | null>(null)
  const [drop, setDrop] = useState<Destination | null>(null)
  const [sortOpen, setSortOpen] = useState(false)
  const [showTags, setShowTags] = useHashState<boolean>('task_show_tags',true)
  const [showDates, setShowDates] = useHashState<boolean>('task_show_dates',true)
  const [fit, setFit] = useHashState<boolean>('task_fit_width', true)
  const [showDone, setShowDone] = useHashState<boolean>('task_show_done', true)
  const [dateTick, setDateTick] = useState(0)
  useEffect(() => { const timer = setInterval(() => setDateTick(value => value + 1), 60000); return () => clearInterval(timer) }, [])
  void dateTick
  const auth = useContext(AuthContext)
  const { ref: scrollRef } = useViewContext({
    userId: auth?.user?.id ?? 'anonymous',
    page: 'project-workspace',
    objectId: projectId,
    region: 'task-board',
    // Existing board content stays mounted during a background/filter request.
    // Re-entering restoration on every loading cycle steals toolbar focus.
    ready: !!board,
  })
  const scope = useRef(0)
  const active = useRef(true)
  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
    }
  }, [])
  const query = boardQuery({ q, sort, owner, milestone, status: '', column: '', tags: tagIds, overdue, dueFrom, dueBefore, due, timezone: project?.business_timezone, priority })
  const queryString = query.toString()
  function failure(cause: unknown) {
    if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
      setBoard(null)
      setCancelledBoard(null)
      setBulk(false)
      setSettings(false)
      setCreating(null)
      setReason(null)
    }
    setError(cause instanceof Error ? cause.message : '操作失败，请重试。')
  }
  useEffect(() => {
    const controller = new AbortController()
    const generation = ++scope.current
    queueMicrotask(() => {
      if (!controller.signal.aborted) {
        setLoading(true)
        setDrag(null)
        setDrop(null)
      }
    })
    const cancelledQuery = new URLSearchParams(queryString)
    cancelledQuery.set('status','cancelled')
    Promise.all([
      getProjectBoard(projectId, new URLSearchParams(queryString), controller.signal),
      getProjectBoard(projectId, cancelledQuery, controller.signal),
    ]).then(([value, cancelled]) => {
        if (!controller.signal.aborted && generation === scope.current) {
          const focusedCard = document.activeElement?.closest<HTMLElement>('.board-card-open')
          setBoard(value)
          setCancelledBoard(cancelled)
          setError('')
          // A saved task can move between lanes after the inspector has closed.
          // Preserve that card's focus when React replaces it; keep any newer user focus.
          if (focusedCard?.id) requestAnimationFrame(() => {
            if (!controller.signal.aborted && generation === scope.current && !focusedCard.isConnected && document.activeElement === document.body)
              document.getElementById(focusedCard.id)?.focus({ preventScroll: true })
          })
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) failure(cause)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [projectId, queryString, retry, refreshKey, stateRevision])
  useEffect(() => {
    if (!notice) return
    const timer = setTimeout(() => setNotice(''), 6000)
    return () => clearTimeout(timer)
  }, [notice])
  const canMove = (task: ProjectTask) =>
    sort === 'manual' &&
    task.status !== 'cancelled' &&
    !pending &&
    !loading &&
    (canManage ||
      !!(
        task.status !== 'done' &&
        project?.actions?.includes('update_own_task') &&
        project.current_member_id &&
        [task.owner_member_id, ...(task.participant_ids ?? [])].includes(project.current_member_id)
      ))
  async function loadMore(column: ProjectBoardPage['columns'][number], cancelled = false) {
    if (busy.current || !column.next_cursor) return
    busy.current = true
    setPending(true)
    const generation = scope.current
    const params = new URLSearchParams(queryString)
    if (cancelled) params.set('status','cancelled')
    params.set('column_id', column.id)
    params.set('cursor', column.next_cursor)
    try {
      const page = await getProjectBoard(projectId, params)
      if (active.current && generation === scope.current) {
        if (page.version !== board?.version) throw new Error('看板配置已变化，请重新加载。')
        const next = page.columns.find((item) => item.id === column.id)!
        const setPage = cancelled ? setCancelledBoard : setBoard
        setPage(
          (current) =>
            current && {
              ...current,
              columns: current.columns.map((item) =>
                item.id === column.id
                  ? {
                      ...next,
                      items: [
                        ...item.items,
                        ...next.items.filter(
                          (task) => !item.items.some((old) => old.id === task.id)
                        ),
                      ],
                    }
                  : item
              ),
            }
        )
      }
    } catch (cause) {
      if (active.current && generation === scope.current) failure(cause)
    } finally {
      busy.current = false
      if (active.current) setPending(false)
    }
  }
  async function move(destination: Destination, blockedReason = '', changeReason = '') {
    if (!board || busy.current || !canMove(destination.task)) return
    setDrag(null)
    setDrop(null)
    const target = board.columns.find((column) => column.id === destination.column)
    const availableBlockedReason = blockedReason.trim() ? blockedReason : destination.task.blocked_reason ?? ''
    if (target?.stage === 'blocked' && !availableBlockedReason.trim()) {
      onOpenTask(destination.task.id, { columnId: target.id })
      return
    }
    if (destination.task.status === 'done' && target?.stage !== 'done' && !changeReason.trim()) {
      setReason(destination)
      return
    }
    busy.current = true
    setPending(true)
    setError('')
    const generation = scope.current
    let persisted = false
    const command = {
      expected_task_version: destination.task.version,
      expected_board_version: board.version,
      target_column_id: destination.column,
      before_id: destination.before ?? null,
      after_id: destination.after ?? null,
      sort: 'manual' as const,
      blocked_reason: target?.stage === 'blocked' ? availableBlockedReason : blockedReason,
      change_reason: changeReason,
    }
    const signature = JSON.stringify([destination.task.id, command])
    if (moveAttempt.current?.signature !== signature)
      moveAttempt.current = { signature, key: crypto.randomUUID() }
    try {
      await moveProjectTask(projectId, destination.task.id, {
        ...command,
        request_key: moveAttempt.current.key,
      })
      persisted = true
      if (active.current) {
        setReason(null)
        if (generation !== scope.current) {
          setRetry((value) => value + 1)
          return
        }
        let fresh = await getProjectBoard(projectId, new URLSearchParams(queryString))
        let targetPage = fresh.columns.find((column) => column.id === destination.column)
        while (
          active.current &&
          generation === scope.current &&
          targetPage?.next_cursor &&
          !targetPage.items.some((task) => task.id === destination.task.id)
        ) {
          const params = new URLSearchParams(queryString)
          params.set('column_id', destination.column)
          params.set('cursor', targetPage.next_cursor)
          const next = await getProjectBoard(projectId, params)
          if (next.version !== fresh.version)
            throw new Error('移动已保存，但看板配置已变化，请重新加载。')
          const nextColumn = next.columns.find((column) => column.id === destination.column)!
          targetPage = { ...nextColumn, items: [...targetPage.items, ...nextColumn.items] }
          fresh = {
            ...fresh,
            columns: fresh.columns.map((column) =>
              column.id === destination.column ? targetPage! : column
            ),
          }
        }
        if (active.current && generation === scope.current) {
          setBoard(fresh)
          setNotice('任务位置已保存。')
          requestAnimationFrame(() => {
            const card = document.getElementById(`board-task-${destination.task.id}`)
            card?.focus({ preventScroll: true })
            card?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
          })
        }
      }
    } catch (cause) {
      if (active.current) {
        if (persisted && !(cause instanceof ApiError && [401, 403, 404].includes(cause.status)))
          setError(
            `移动已保存，重新读取失败：${cause instanceof Error ? cause.message : '请重新加载看板。'}`
          )
        else failure(cause)
      }
    } finally {
      busy.current = false
      if (active.current) setPending(false)
    }
  }
  function keyboard(
    event: KeyboardEvent,
    task: ProjectTask,
    columnIndex: number,
    taskIndex: number
  ) {
    if (event.key === 'Escape') {
      setDrag(null)
      setDrop(null)
      return
    }
    if (!event.altKey || !event.key.startsWith('Arrow') || !canMove(task) || !board) return
    event.preventDefault()
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      const target = board.columns[columnIndex + (event.key === 'ArrowLeft' ? -1 : 1)]
      if (target) void move({ task, column: target.id })
    } else {
      const next = board.columns[columnIndex].items[taskIndex + (event.key === 'ArrowUp' ? -1 : 1)]
      if (next)
        void move({
          task,
          column: task.column_id!,
          ...(event.key === 'ArrowUp' ? { before: next.id } : { after: next.id }),
        })
    }
  }
  function over(event: DragEvent, column: string, anchor?: ProjectTask) {
    if (!drag || !canMove(drag) || anchor?.id === drag.id) return
    event.preventDefault()
    event.stopPropagation()
    event.dataTransfer.dropEffect = 'move'
    const before =
      anchor &&
      event.clientY <
        event.currentTarget.getBoundingClientRect().top +
          event.currentTarget.getBoundingClientRect().height / 2
    setDrop({
      task: drag,
      column,
      ...(anchor ? (before ? { before: anchor.id } : { after: anchor.id }) : {}),
    })
  }
  const columns = board?.columns ?? []
  const visibleColumns = columns.filter(item => status !== 'cancelled' && (!column || item.id === column) && (!status || item.stage === status) && (status || column || showDone || item.stage !== 'done'))
  const cancelledCount = cancelledBoard?.total ?? 0
  const cancelledId = '__archived_cancelled__'
  if (cancelledBoard?.columns.length && (status === 'cancelled' || (!column && !status && cancelledCount))) {
    visibleColumns.push({...cancelledBoard.columns[0], id:cancelledId, name:'已取消', color:'#798294', items:cancelledBoard.columns.flatMap(item=>item.items), total:cancelledCount, next_cursor:null})
  }
  const tabs = [
    {id:'', name:'全部', count:(board?.total ?? 0) + cancelledCount},
    ...columns.map(item=>({id:item.id,name:item.name,count:item.total})),
    {id:cancelledId,name:'已取消',count:cancelledCount},
  ]
  const currentTab = status === 'cancelled' ? cancelledId : column || columns.find(item=>item.stage===status)?.id || ''
  function selectTab(id:string) {
    updateHashParams({task_column:id && id!==cancelledId ? id : null,task_status:id===cancelledId ? 'cancelled' : null})
  }
  const filtered = Boolean(q || owner || milestone || status || column || tagIds.length || due || overdue || dueFrom || dueBefore || priority)
  const clear = () => {
    updateHashParams(Object.fromEntries(['task_q','task_owner','task_milestone','task_status','task_column','task_tag','task_tags','task_due','task_due_from','task_due_before','task_overdue','task_priority'].map(key => [key, null])))
    requestAnimationFrame(() => searchInput.current?.focus({ preventScroll: true }))
  }
  const people = [{ id: '', name: '全部负责人', organization: '' }, { id: 'unassigned', name: '未分配', organization: '' }, ...(project?.members ?? []).map(member => ({ id: member.id, name: member.display_name, organization: member.organization_path }))]
  return <section className="project-task-board" aria-label="正式任务看板" aria-busy={loading || pending} onKeyDown={event => { if (event.key === 'Escape') { setDrag(null); setDrop(null) } }}>
    <header className="task-page-header">
      <div className="task-page-heading"><div><h2>全部任务</h2><p>共享交付要求与成果；每位成员可以选择自己的处理方式。</p></div>
        {project?.actions.includes('create_task') && <Button className="task-page-create" onClick={()=>setCreating('')}><Plus size={15} aria-hidden />新建任务</Button>}
      </div>
      <div className="task-page-tools">
        <div className="task-page-query">{navigation}
          <label className="task-page-search"><Search size={15} aria-hidden /><Input aria-label="搜索任务" ref={searchInput} type="search" placeholder="搜索编号、任务名、项目名或负责人" value={q} onChange={event=>setQ(event.target.value)} /></label>
          <Popover><PopoverTrigger asChild><Button variant="outline"><Funnel size={15} aria-hidden />筛选</Button></PopoverTrigger><PopoverContent className="board-advanced-filters task-page-filter-panel" align="start" aria-label="筛选任务">
        <div className="board-owner-filter"><SearchCombobox items={people} getKey={item => item.id} getLabel={item => item.name} getDescription={item => item.organization} getSearchText={item => peopleSearchText({name:item.name,organizationPath:item.organization})} label="按负责人筛选" placeholder="全部负责人" value={owner} onChange={value => setOwner(value ?? '')} /></div>
        <NativeSelect aria-label="筛选里程碑" value={milestone} onChange={event => setMilestone(event.target.value)}>
          <option value="">全部里程碑</option><option value="@none">未关联里程碑</option>
          {project?.milestones?.map(node => <option key={node.id} value={node.id}>{node.title}</option>)}
          {milestone && milestone !== '@none' && !project?.milestones?.some(node => node.id === milestone) && <option value={milestone}>{milestone}</option>}
        </NativeSelect>
        <NativeSelect aria-label="按截止日期筛选" value={due || (overdue ? 'overdue' : '')} onChange={event => { updateHashParams({ task_due: event.target.value || null, task_overdue: null, task_due_from: null, task_due_before: null }) }}><option value="">全部期限</option><option value="overdue">已逾期</option><option value="today">今天截止</option><option value="week">未来 7 天</option><option value="none">无截止日期</option></NativeSelect>
        <TaskTagPicker tags={board?.tags ?? []} value={tagIds} onChange={values => updateHashParams({ task_tags: values.length ? JSON.stringify(values) : null, task_tag: null })} label="按标签筛选" />
<label>优先级<NativeSelect value={priority} onChange={event => setPriority(event.target.value)}><option value="">全部优先级</option><option value="unset">未设置</option>{Object.entries({P0:'紧急',P1:'高',P2:'中',P3:'低'}).map(([value,label]) => <option key={value} value={value}>{label}</option>)}</NativeSelect></label><label>截止开始（UTC）<Input type="date" value={dueFrom} onChange={event => { setDueFrom(event.target.value); setDue(''); updateHashParams({task_overdue:null}) }} /></label><label>截止结束（UTC，不含）<Input type="date" value={dueBefore} onChange={event => { setDueBefore(event.target.value); setDue(''); updateHashParams({task_overdue:null}) }} /></label>
            {filtered && <Button variant="outline" onClick={clear}>清空筛选</Button>}
            <details><summary>移动操作说明</summary>拖动卡片或按 Alt + 方向键移动任务。</details>
          </PopoverContent></Popover>
          {canManage && <Button variant="ghost" disabled={!board || !visibleColumns.some(c=>c.items.length)} onClick={()=>setBulk(true)}><ListChecks size={15} aria-hidden />批量操作</Button>}
        </div>
        <div className="task-page-display"><div className="task-layout-switch" role="group" aria-label="显示方式">
          <button type="button" aria-pressed={layout==='list'} onClick={()=>setLayout('list')}><List size={14} aria-hidden />列表</button>
          <button type="button" aria-pressed={layout==='board'} onClick={()=>setLayout('board')}><Columns3 size={14} aria-hidden />看板</button>
        </div>
          <Popover open={sortOpen} onOpenChange={setSortOpen}><PopoverTrigger asChild><Button ref={sortTrigger} className="board-toolbar-icon" variant="outline" aria-label="任务排序" title="任务排序" data-active={sort !== 'manual' || undefined}><ArrowUpDown size={15} aria-hidden /></Button></PopoverTrigger><PopoverContent align="end" className="board-choice-menu" aria-label="任务排序"><p>排序方式</p><fieldset><legend className="sr-only">排序方式</legend>{Object.entries({manual:'手动顺序',priority:'优先级',due:'截止日期',title:'任务名称'}).map(([value,label])=><label key={value}><input type="radio" name="task-sort" value={value} checked={sort===value} onChange={()=>{setSort(value as TaskSort);setSortOpen(false);requestAnimationFrame(()=>sortTrigger.current?.focus({preventScroll:true}))}} />{label}</label>)}</fieldset></PopoverContent></Popover>
          <Popover open={fieldsOpen} onOpenChange={setFieldsOpen}><PopoverTrigger asChild><Button id="board-fields-trigger" variant="outline" aria-label="展示字段" title="展示字段"><Settings size={15} aria-hidden />展示字段</Button></PopoverTrigger><PopoverContent align="end" className="board-choice-menu" aria-label="展示字段"><p>显示选项</p><label><input type="checkbox" checked={showDone} onChange={event=>setShowDone(event.target.checked)} />显示已完成列</label><label><input type="checkbox" checked={fit} onChange={event=>setFit(event.target.checked)} />铺满可用宽度</label><label><input type="checkbox" checked={showTags} onChange={event=>setShowTags(event.target.checked)} />显示标签</label><label><input type="checkbox" checked={showDates} onChange={event=>setShowDates(event.target.checked)} />显示日期</label>{(project?.actions?.includes('configure_board') ?? canManage) && <Button variant="ghost" disabled={!board || loading} onClick={() => {setFieldsOpen(false);setSettingsColumn(undefined);setSettings(true)}}>看板设置</Button>}</PopoverContent></Popover>
        </div>
      </div>
      <div className="task-page-status"><div className="task-status-tabs" role="group" aria-label="任务状态筛选">
        {tabs.map(tab=><button key={tab.id} type="button" aria-pressed={currentTab===tab.id} onClick={()=>selectTab(tab.id)}>{tab.name}<span>{board ? tab.count : '—'}</span></button>)}
      </div><span className="task-page-total" role="status">{board ? `${visibleColumns.reduce((sum,item)=>sum+item.total,0)} 项任务` : '正在加载…'}</span></div>
    </header>
    {bulk && <TaskBulkEdit projectId={projectId} tasks={visibleColumns.flatMap(item=>item.items)} onClose={()=>setBulk(false)} onSaved={()=>{setBulk(false);setRetry(n=>n+1);setNotice('所选任务的优先级已更新。')}}/>}
    {sort !== 'manual' && <p className="board-help ui-hint" role="status">当前排序禁止手动移动，请先切换手动排序。</p>}
    {error && <div className="board-notice error" role="alert">{error}<Button variant="outline" onClick={() => setRetry(value => value + 1)}>重新加载看板</Button></div>}
    {notice && <div className="board-notice" role="status">{notice}<Button variant="ghost" onClick={() => setNotice('')}>关闭提示</Button></div>}
    <div ref={scrollRef} className={`board-scroll${layout==="list" ? " task-list-layout" : ""}`} role="region" aria-label="任务状态列" tabIndex={0}>
      <div className={`board-columns${!fit ? ' board-fixed' : ''}${visibleColumns.length === 1 ? ' board-single' : ''}`}>
        {visibleColumns.map(item => { const ci = board!.columns.findIndex(source => source.id === item.id); return <section key={item.id} className="board-lane" role="region" aria-label={item.name} data-drop-target={drop?.column === item.id || undefined} onDragOver={event => {if(item.id!==cancelledId) over(event,item.id)}} onDrop={event => { event.preventDefault(); if (item.id!==cancelledId && drop) void move(drop) }}>
          <header><span className="board-dot" style={{background:item.color}} /><h3>{item.name}</h3><span className="board-count">{item.total}</span>{item.id !== cancelledId && <DropdownMenu><DropdownMenuTrigger asChild><button type="button" id={'board-menu-'+item.id} className="board-lane-menu" aria-label={`${item.name}列操作`}><MoreHorizontal size={15} aria-hidden /></button></DropdownMenuTrigger><DropdownMenuContent align="end" aria-label={`${item.name}列操作`}><DropdownMenuItem onSelect={() => { setColumn(column === item.id ? '' : item.id); setStatus('') }}>{column === item.id ? '查看全部状态' : '只看此列'}</DropdownMenuItem>{(project?.actions?.includes('configure_board') ?? canManage) && <DropdownMenuItem onSelect={() => { setSettingsColumn(item.id); setSettings(true) }}>编辑状态列</DropdownMenuItem>}</DropdownMenuContent></DropdownMenu>}{item.id !== cancelledId && (canManage || project?.actions.includes('create_task') && item.stage==='todo') && project && <button className="board-lane-add" type="button" aria-label={`在「${item.name}」新建任务`} onClick={() => setCreating(item.id)}><Plus size={15} aria-hidden /></button>}</header>
          <div className="board-cards">{item.items.map((task,ti) => { const ownerName = task.owner_member_id ? project?.members.find(member => member.id === task.owner_member_id)?.display_name ?? '成员姓名未知' : '未分配'; const cardTags = task.tag_ids?.map(id => board!.tags.find(tag => tag.id === id)).filter((tag): tag is NonNullable<typeof tag> => Boolean(tag)) ?? []; return <article key={task.id} className="board-card" data-dragging={drag?.id === task.id || undefined} data-insert={drop?.before === task.id ? 'before' : drop?.after === task.id ? 'after' : undefined} draggable={canMove(task)} onDragStart={event => { if (!canMove(task)) { event.preventDefault(); return }; event.dataTransfer.setData('text/plain', task.id); event.dataTransfer.effectAllowed='move'; setDrag(task) }} onDragOver={event => {if(item.id!==cancelledId) over(event,item.id,task)}} onDragEnd={() => { setDrag(null); setDrop(null) }}>
            <button type="button" id={`board-task-${task.id}`} className="board-card-open" data-view-trigger={task.id} aria-haspopup="dialog" aria-keyshortcuts={canMove(task) ? 'Alt+ArrowUp Alt+ArrowDown Alt+ArrowLeft Alt+ArrowRight' : undefined} aria-label={`T-${String(task.display_number ?? '').padStart(3,'0')} ${task.title}`} onClick={() => onOpenTask(task.id)} onKeyDown={event => keyboard(event,task,ci,ti)}>
              <span className="board-card-top"><small>T-{String(task.display_number ?? '').padStart(3,'0')}</small><span className="board-card-owner">{task.owner_member_id && <span className="board-avatar" aria-hidden>{Array.from(ownerName).slice(-2).join('')}</span>}{ownerName}</span></span>
              {(task.priority || (showTags && cardTags.length > 0)) && <span className="board-tags">{task.priority && <span className="board-priority" data-priority={task.priority}>{{P0:'紧急',P1:'高',P2:'中',P3:'低'}[task.priority]}</span>}{showTags && <>{cardTags.slice(0,2).map(tag => <span key={tag.id} title={`${tag.type} / ${tag.name}`}>{tag.name}</span>)}{cardTags.length > 2 && <span title={cardTags.slice(2).map(tag => `${tag.type} / ${tag.name}`).join('、')}>+{cardTags.length - 2}</span>}</>}</span>}
              <strong>{task.title}</strong>{task.blocked_reason && <span className="board-blocker" title={task.blocked_reason}>{task.blocked_reason}</span>}
              {showDates && <time dateTime={task.due_at ?? undefined} className={task.overdue ? 'board-overdue' : ''} title={task.due_at ?? undefined}>{task.overdue ? '逾期 ' : ''}{task.due_at ? taskDate(task.due_at,project?.business_timezone ?? 'UTC').slice(5).replace('-', '/') : '无截止日期'}</time>}
            </button>
            {(canManage || project?.actions?.includes('update_own_task')) && <Popover><PopoverTrigger asChild><button className="board-card-action" type="button" aria-label={`任务操作：${task.title}`}><MoreHorizontal size={14} aria-hidden /></button></PopoverTrigger><PopoverContent align="end" className="board-card-actions" aria-label={`任务操作：${task.title}`}><label>移动到<NativeSelect aria-label={`移动${task.title}`} value={task.column_id} disabled={!canMove(task)} onChange={event => void move({task,column:event.target.value})}>{board!.columns.map(column => <option key={column.id} value={column.id}>{column.name}</option>)}</NativeSelect></label></PopoverContent></Popover>}
          </article>})}{!item.items.length && <p className="board-empty">{filtered ? '无匹配任务' : '暂无任务'}</p>}{item.id === cancelledId && cancelledBoard!.columns.filter(source=>source.next_cursor).map(source=><Button key={source.id} variant="outline" disabled={pending||loading} onClick={()=>void loadMore(source,true)}>继续加载已取消任务（{source.name}）</Button>)}{item.next_cursor && <Button variant="outline" disabled={pending || loading} onClick={() => void loadMore(item)}>继续加载{item.name}</Button>}</div>
        </section>})}
      </div>
    </div>
    {settings && board && <ProjectBoardSettings projectId={projectId} board={board} initialColumnId={settingsColumn} onDenied={failure} onClose={() => setSettings(false)} onSaved={() => { setSettings(false); setRetry(value => value+1); setNotice('看板设置已保存，列中全部任务已按配置迁移。') }} />}
    {creating !== null && project && <ProjectTaskInspector project={project} taskId="" mode="create" initialColumnId={creating || board?.default_column_id} canManage={canManage} onClose={() => setCreating(null)} onChanged={() => setRetry(value => value+1)} onCreated={() => { setCreating(null); setNotice('正式任务已创建。') }} />}
    {reason && <MoveReason blocked={board?.columns.find(item => item.id === reason.column)?.stage === 'blocked' && !reason.task.blocked_reason?.trim()} destination={reason} pending={pending} error={error} onClose={() => setReason(null)} onSave={(blockedReason,changeReason) => void move(reason,blockedReason,changeReason)} />}
  </section>
}

function MoveReason({
  blocked,
  destination,
  pending,
  error,
  onClose,
  onSave,
}: {
  blocked: boolean
  destination: Destination
  pending: boolean
  error: string
  onClose: () => void
  onSave: (reason: string, change: string) => void
}) {
  return (
    <ProjectDialog title="核对任务状态变更" busy={pending} onClose={onClose}>
      <form
        className="project-form"
        onSubmit={(event) => {
          event.preventDefault()
          const data = new FormData(event.currentTarget)
          onSave(String(data.get('blocked_reason') ?? ''), String(data.get('change_reason') ?? ''))
        }}
      >
        <p>{destination.task.title}</p>
        {blocked && (
          <label>
            受阻原因
            <Textarea name="blocked_reason" required maxLength={4000} />
          </label>
        )}
        {destination.task.status === 'done' && (
          <label>
            重新打开原因
            <Textarea name="change_reason" required maxLength={4000} />
          </label>
        )}
        {error && <p role="alert">{error}</p>}
        <div className="dialog-actions">
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={pending}>
            确认移动
          </Button>
        </div>
      </form>
    </ProjectDialog>
  )
}
