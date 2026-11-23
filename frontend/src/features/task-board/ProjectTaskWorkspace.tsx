import { useProjectRevision } from './project-state'
import { Textarea } from './controls'
import { Input } from './controls'
import { NativeSelect } from './controls'
import { Button, DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from './controls'
import { ChevronDown } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type ReactNode, type FormEvent } from 'react'

import { ApiError } from './api'
import { SearchCombobox } from './search-combobox'
import { peopleSearchText } from './people-search'
import { Popover, PopoverContent, PopoverTrigger } from './controls'
import { useHashState, updateHashParams } from './hash-state'
import { ArrowDownWideNarrow, Columns3, SlidersHorizontal, Plus, Search, CircleHelp } from 'lucide-react'
import { TaskTagPicker } from './TaskTagPicker'
import { boardQuery, taskTagIds } from './task-board-filters'
import { ProjectBoardSettings } from './ProjectBoardSettings'
import { ProjectTaskBoard } from './ProjectTaskBoard'
import { ProjectDialog } from './ProjectDialog'
import { ProjectTaskInspector, type TaskEditIntent } from './ProjectTaskInspector'
import { ProjectTaskEditor } from './ProjectTasks'
import { getProjectBoard, type ProjectBoardPage, type TaskSort, getProjectCandidates, getProjectTask, getProjectTasks, reviewProjectCandidate, type ProjectDetail, type ProjectTask, type TaskCandidate, type TaskFields } from './api'

function viewFromUrl() { const query = new URLSearchParams(window.location.hash.split('?')[1]); return !query.get('task') && (query.get('task_view') === 'discovery' || (!query.has('task_view') && query.has('candidate'))) ? 'discovery' : 'confirmed' }

export function ProjectTaskWorkspace({ project, canManage }: { project: ProjectDetail; canManage: boolean }) {
  const stateRevision = useProjectRevision('tasks')
  const [view, setView] = useState(() => viewFromUrl())
  const [taskIntent, setTaskIntent] = useState<TaskEditIntent | null>(null)
  const [taskId, setTaskId] = useState(() => new URLSearchParams(window.location.hash.split('?')[1]).get('task') ?? '')
  const boardDock = useRef<HTMLDivElement>(null)
  const boardPosition = useRef<{ left: number; top: number } | null>(null)
  const [boardRevision, setBoardRevision] = useState(0)
  const [countRevision, setCountRevision] = useState(0)
  const onReviewed = useCallback(() => setCountRevision(value => value + 1), [])
  const [candidateCount, setCandidateCount] = useState<number | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    async function count() {
      const result = await getProjectCandidates(project.id, 'pending_review', '', controller.signal, undefined, 1)
      if (!controller.signal.aborted) setCandidateCount(result.total ?? null)

    }
    void count().catch(() => { if (!controller.signal.aborted) setCandidateCount(null) })
    return () => controller.abort()
  }, [project.id, view, countRevision, stateRevision])
  function openTask(id: string, intent?: TaskEditIntent) {
    if (id && taskId && id !== taskId && !window.dispatchEvent(new CustomEvent('omni-grid:before-route-change', { cancelable: true }))) return
    const scroll = boardDock.current?.querySelector<HTMLElement>('.task-list-layout .list-scroll') ?? boardDock.current?.querySelector<HTMLElement>('.board-scroll')
    if (id && !taskId && scroll) boardPosition.current = { left: scroll.scrollLeft, top: scroll.scrollTop }
    const prior = taskId
    const position = boardPosition.current ?? { left: scroll?.scrollLeft ?? 0, top: scroll?.scrollTop ?? 0 }
    setTaskIntent(intent ?? null)
    setTaskId(id)
    if (!id) requestAnimationFrame(() => {
      const restore = () => {
        (document.getElementById(`board-task-${prior}`) ?? scroll)?.focus({ preventScroll: true })
        if (scroll) { scroll.scrollLeft = position.left; scroll.scrollTop = position.top }
      }
      restore()
      requestAnimationFrame(restore)
    })
    const query = new URLSearchParams(window.location.hash.split('?')[1])
    if (id) query.set('task', id); else { query.delete('task'); boardPosition.current = null }
    query.set('task_view', 'confirmed')
    window.history.replaceState(history.state, '', `${location.hash.split('?')[0]}?${query}`)
    setView('confirmed')
  }
  useEffect(() => { const sync = () => { setView(viewFromUrl()); setTaskId(new URLSearchParams(window.location.hash.split('?')[1]).get('task') ?? '') }; window.addEventListener('hashchange', sync); return () => window.removeEventListener('hashchange', sync) }, [canManage])
  function select(next: 'discovery' | 'confirmed') {
    const probe = new CustomEvent('omni-grid:before-route-change', { cancelable: true }); if (!window.dispatchEvent(probe)) return
    setView(next)
    const query = new URLSearchParams(window.location.hash.split('?')[1]); query.set('task_view', next)
    if (next === 'discovery') { query.delete('task'); setTaskId('') }
    window.history.replaceState(history.state, '', `${location.hash.split('?')[0]}?${query}`)
  }
  const navigation = <DropdownMenu><DropdownMenuTrigger asChild><Button variant="outline" id="task-scope-menu" aria-label="选择任务范围">
    {view === 'confirmed' ? '全部任务' : '待确认任务'}<ChevronDown size={14} aria-hidden />
  </Button></DropdownMenuTrigger><DropdownMenuContent align="start" aria-label="任务范围">
    <DropdownMenuItem onSelect={() => select('confirmed')}>全部任务</DropdownMenuItem>
    <DropdownMenuItem onSelect={() => select('discovery')}>待确认任务{candidateCount !== null && `（${candidateCount}）`}</DropdownMenuItem>
  </DropdownMenuContent></DropdownMenu>
  return <div className="project-task-workspace-root">
    <div id="project-task-view-panel" role="region" aria-label={view === 'confirmed' ? '正式任务' : '待确认'}>
      {view === 'confirmed' ? <div ref={boardDock} className="board-inspector-dock"><ProjectTaskBoard key={project.id} projectId={project.id} project={project} refreshKey={boardRevision} navigation={navigation} canManage={canManage} onOpenTask={openTask} />{taskId && <ProjectTaskInspector key={`${project.id}:${taskId}`} project={project} taskId={taskId} initialColumnId={taskIntent?.columnId} canManage={canManage} onChanged={() => setBoardRevision(value => value + 1)} onClose={() => openTask('')} />}</div> : <DiscoveryPanel navigation={navigation} project={project} canManage={canManage} onTask={openTask} onReviewed={onReviewed} />}
    </div>
  </div>
}

function DiscoveryPanel({ project, canManage, onTask, onReviewed, navigation }: { navigation: ReactNode; project: ProjectDetail; canManage: boolean; onTask: (id: string) => void; onReviewed: () => void }) {
  const stateRevision = useProjectRevision('tasks')
  const [q] = useHashState<string>('candidate_q','')
  const [owner] = useHashState<string>('candidate_owner','')
  const [due] = useHashState<string>('candidate_due','')
  const [rawTags] = useHashState<string>('candidate_tags','')
  const [sort] = useHashState<TaskSort>('candidate_sort','manual',['manual','priority','due','title'])
  const [showTags, setShowTags] = useHashState<boolean>('candidate_show_tags',true)
  const [showDates, setShowDates] = useHashState<boolean>('candidate_show_dates',true)
  const [board, setBoard] = useState<ProjectBoardPage | null>(null)
  const [total, setTotal] = useState<number | null>(null)
  const [boardSettings, setBoardSettings] = useState(false)
  const [creating, setCreating] = useState(false)
  const [sortOpen, setSortOpen] = useState(false)
  const sortTrigger = useRef<HTMLButtonElement>(null)
  const tagIds = taskTagIds(rawTags,'')
  const filters = boardQuery({q,owner,due,sort,tags:tagIds,status:'',column:'',overdue:false,dueFrom:'',dueBefore:'',timezone:project.business_timezone})
  const filterQuery = filters.toString()
  useEffect(() => {
    const controller = new AbortController()
    getProjectBoard(project.id,new URLSearchParams({limit:'1'}),controller.signal).then(value => {if(!controller.signal.aborted)setBoard(value)}).catch(() => {if(!controller.signal.aborted)setBoard(null)})
    return () => controller.abort()
  },[project.id,stateRevision,boardSettings])

  const [items, setItems] = useState<TaskCandidate[]>([])
  const [cursor, setCursor] = useState(() => new URLSearchParams(window.location.hash.split('?')[1]).get('candidate_cursor') ?? '')
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [filter, setFilter] = useState(candidateStatusFromHash)
  const [selected, setSelected] = useState<TaskCandidate | null>(null)
  const [dialog, setDialog] = useState<{ mode: 'confirm' | 'ignore' | 'merge'; candidate: TaskCandidate } | null>(null)
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const listScroll = useRef(0)
  useEffect(() => {
    const controller = new AbortController()
    async function refresh() {
      try {
        const candidates = await getProjectCandidates(project.id, filter, cursor, controller.signal, new URLSearchParams(filterQuery))
        if(controller.signal.aborted)return
        setItems(candidates.items); setNextCursor(candidates.next_cursor); setTotal(candidates.total ?? null); onReviewed()
        setSelected(candidates.items.find((item) => item.id === new URLSearchParams(window.location.hash.split('?')[1]).get('candidate')) ?? null)
        setError('')
      } catch (cause) { if (!controller.signal.aborted) { if (cause instanceof ApiError && [401, 403,404].includes(cause.status)) { setItems([]); setSelected(null); setDialog(null) } setError(cause instanceof Error ? cause.message : '提炼状态加载失败，请重试。') } }
    }
    void refresh()
    return () => { controller.abort() }
  }, [project.id, filter, cursor, retry, onReviewed, stateRevision, filterQuery])
  useEffect(() => { const sync = () => { setFilter(candidateStatusFromHash()); setCursor(new URLSearchParams(window.location.hash.split('?')[1]).get('candidate_cursor') ?? ''); setSelected(null); setRetry((value) => value + 1) }; window.addEventListener('hashchange', sync); return () => window.removeEventListener('hashchange', sync) }, [])
  const selectedId = selected?.id
  useEffect(() => { if (selectedId) requestAnimationFrame(() => document.getElementById('candidate-detail-title')?.focus()) }, [selectedId])
  function candidateUrl(status = filter, page = cursor, id = selected?.id ?? '') { const query = new URLSearchParams(window.location.hash.split('?')[1]); for (const [key, value] of Object.entries({ candidate_status: status, candidate_cursor: page, candidate: id })) { if (value) query.set(key, value); else query.delete(key) }; window.history.replaceState(history.state, '', `${location.hash.split('?')[0]}?${query}`) }
  const candidateLabels: Record<string,string> = {pending_review:'待确认',confirmed:'已确认',ignored:'已忽略',merged:'已合并',superseded:'来源已修订'}
  return <section className="project-discovery task-review-board" aria-label="待确认任务建议">
    <div className="board-tools"><div className="board-toolbar">{navigation}<div className="board-toolbar-group">
      <Popover open={sortOpen} onOpenChange={setSortOpen}><PopoverTrigger asChild><Button ref={sortTrigger} className="board-toolbar-icon" variant="outline" aria-label="任务建议排序"><ArrowDownWideNarrow size={15} aria-hidden /></Button></PopoverTrigger><PopoverContent align="end" className="board-choice-menu"><p>排序方式</p>{Object.entries({manual:'手动顺序',priority:'优先级',due:'截止日期',title:'任务名称'}).map(([value,label])=><label key={value}><input type="radio" name="candidate-sort" checked={sort===value} onChange={()=>{updateHashParams({candidate_sort:value,candidate_cursor:null,candidate:null});setSortOpen(false);requestAnimationFrame(()=>sortTrigger.current?.focus({preventScroll:true}))}} />{label}</label>)}</PopoverContent></Popover>
      <Popover><PopoverTrigger asChild><Button className="board-toolbar-icon" variant="outline" aria-label="任务建议显示选项"><Columns3 size={15} aria-hidden /></Button></PopoverTrigger><PopoverContent align="end" className="board-choice-menu"><label><input type="checkbox" checked={showTags} onChange={event=>setShowTags(event.target.checked)} />显示标签</label><label><input type="checkbox" checked={showDates} onChange={event=>setShowDates(event.target.checked)} />显示截止日期</label></PopoverContent></Popover>
      {canManage && <><Button variant="outline" disabled={!board} onClick={()=>setBoardSettings(true)}><SlidersHorizontal size={15} aria-hidden />看板设置</Button><Button onClick={()=>setCreating(true)}><Plus size={15} aria-hidden />新建任务</Button></>}
      <Popover><PopoverTrigger asChild><Button variant="outline" aria-label="提炼与审核设置"><CircleHelp size={15} aria-hidden /></Button></PopoverTrigger><PopoverContent align="end" className="task-review-settings"><p className="ui-hint">展示独立 DSH 提交的任务建议，人工核对后纳入正式任务。</p><label>建议状态<NativeSelect value={filter} onChange={event=>{setFilter(event.target.value);setCursor('');setSelected(null);candidateUrl(event.target.value,'','')}}>{Object.entries(candidateLabels).map(([key,label])=><option value={key} key={key}>{label}</option>)}</NativeSelect></label></PopoverContent></Popover>
    </div></div>
    <div className="board-filters"><label className="board-search"><span className="sr-only">搜索任务建议</span><Search size={13} aria-hidden /><Input type="search" placeholder="搜索任务建议" value={q} onChange={event=>{updateHashParams({candidate_q:event.target.value||null,candidate_cursor:null,candidate:null})}} /></label><div className="board-owner-filter"><SearchCombobox items={[{id:'',name:'全部负责人',organization:''},{id:'unassigned',name:'未分配',organization:''},...project.members.map(member=>({id:member.id,name:member.display_name,organization:member.organization_path}))]} getKey={item=>item.id} getLabel={item=>item.name} getDescription={item=>item.organization} getSearchText={item=>peopleSearchText({name:item.name,organizationPath:item.organization})} label="按建议负责人筛选" value={owner} onChange={value=>{updateHashParams({candidate_owner:value||null,candidate_cursor:null,candidate:null})}} /></div><NativeSelect aria-label="按建议截止日期筛选" value={due} onChange={event=>{setCursor('');updateHashParams({candidate_due:event.target.value||null,candidate_cursor:null})}}><option value="">全部期限</option><option value="overdue">已逾期</option><option value="today">今天截止</option><option value="week">未来 7 天</option><option value="none">无截止日期</option></NativeSelect><TaskTagPicker tags={board?.tags??[]} value={tagIds} label="按建议标签筛选" onChange={value=>{setCursor('');updateHashParams({candidate_tags:value.length?JSON.stringify(value):null,candidate_cursor:null})}} /><span className="board-result">{total===null?'总数未知':`${total} 项任务`}</span></div></div>
    {error && <div role="alert"><p>{error}</p><Button type="submit" variant="outline" onClick={() => setRetry((value) => value + 1)}>重新加载状态</Button></div>}
    <div className={`project-candidate-workspace${selected ? ' has-candidate-selection' : ''}`}>
      <div className="project-candidate-master board-lane task-review-lane"><header><span className="board-dot" style={{background:'#7c58bc'}} /><h3>{candidateLabels[filter]}</h3><span>{total===null?'?':total}</span></header>{items.length ? <ul className="project-candidate-list board-cards">{items.map(candidate=><li className="board-card" key={candidate.id}><button type="button" id={`candidate-${candidate.id}`} className="board-card-open" aria-pressed={selected?.id===candidate.id} onClick={()=>{listScroll.current=window.scrollY;setSelected(candidate);candidateUrl(filter,cursor,candidate.id);requestAnimationFrame(()=>document.getElementById('candidate-detail-title')?.focus())}}><span className="board-card-top"><small>{candidateLabels[candidate.status]}</small><span className="board-card-owner">{candidate.fields.owner_member_id ? project.members.find(member=>member.id===candidate.fields.owner_member_id)?.display_name??'负责人未知':'未分配'}</span></span>{showTags && <span className="board-tags">{candidate.fields.priority && <span className="board-priority" data-priority={candidate.fields.priority}>{{P0:'紧急',P1:'高',P2:'中',P3:'低'}[candidate.fields.priority]}</span>}{(candidate.fields.tag_ids??[]).slice(0,2).map(id=><span key={id}>{board?.tags.find(tag=>tag.id===id)?.name??'标签名称未知'}</span>)}{(candidate.fields.tag_ids?.length??0)>2 && <span>+{candidate.fields.tag_ids!.length-2}</span>}</span>}<strong>{candidate.fields.title}</strong>{showDates && <time dateTime={candidate.fields.due_at??undefined}>{candidate.fields.due_at?new Intl.DateTimeFormat('zh-CN',{timeZone:project.business_timezone||'UTC',month:'2-digit',day:'2-digit'}).format(new Date(candidate.fields.due_at)):'无截止日期'}</time>}</button></li>)}</ul> : !error ? <p className="section-empty">当前权限和来源版本范围内暂无此类候选。</p> : null}{(cursor||nextCursor) && <div className="project-minutes-pagination"><Button variant="outline" disabled={!cursor} onClick={()=>{setCursor('');setSelected(null);candidateUrl(filter,'','')}}>候选第一页</Button><Button variant="outline" disabled={!nextCursor} onClick={()=>{setCursor(nextCursor??'');setSelected(null);candidateUrl(filter,nextCursor??'','')}}>下一页候选</Button></div>}</div>

      {selected && <div className="project-candidate-detail"><Button type="submit" variant="ghost" onClick={() => { const id = selected.id; setSelected(null); candidateUrl(filter, cursor, ''); requestAnimationFrame(() => { document.getElementById(`candidate-${id}`)?.focus({ preventScroll: true }); window.scrollTo(0, listScroll.current) }) }}>返回建议列表</Button><h4 id="candidate-detail-title" tabIndex={-1}>{selected.fields.title}</h4><p>{selected.fields.description || '请结合依据核对行动内容。'}</p><p>验收建议：{selected.fields.acceptance || '尚未明确，确认时需要补充。'}</p><p className="field-hint">模型置信参考：{Math.round(selected.fields.confidence * 100)}%，需人工核对，不代表准确率。</p><CandidateEvidenceView candidate={selected} />{selected.task_id ? <Button type="submit" variant="outline" onClick={() => onTask(selected.task_id!)}>查看正式任务</Button> : canManage && selected.status === 'pending_review' && <div className="project-candidate-actions"><Button type="submit" variant="outline" onClick={() => setDialog({ mode: 'confirm', candidate: selected })}>编辑并确认任务</Button><Button type="submit" variant="outline" onClick={() => setDialog({ mode: 'merge', candidate: selected })}>合并到已有任务</Button><Button type="submit" variant="ghost" onClick={() => setDialog({ mode: 'ignore', candidate: selected })}>忽略建议</Button></div>}</div>}
    </div>
    {boardSettings && board && <ProjectBoardSettings projectId={project.id} board={board} onClose={()=>setBoardSettings(false)} onDenied={cause=>{setError(cause instanceof Error?cause.message:"看板配置读取失败。");if(cause instanceof ApiError && [401,403,404].includes(cause.status)){setBoard(null);setSelected(null);setDialog(null);setBoardSettings(false)}}} onSaved={()=>{setBoardSettings(false);setRetry(value=>value+1)}} />}
    {creating && <ProjectTaskInspector project={project} taskId="" mode="create" canManage={canManage} onChanged={()=>onReviewed()} onClose={()=>setCreating(false)} onCreated={task=>{setCreating(false);onTask(task.id)}} />}
    {dialog?.mode === 'confirm' && <ProjectTaskEditor project={project} task={null} title="确认候选任务" initial={{ ...dialog.candidate.fields, owner_member_id: dialog.candidate.fields.owner_member_id ?? '' }} requireShare onClose={() => setDialog(null)} onCreate={async (fields: TaskFields) => { try { const result = await reviewProjectCandidate(project.id, dialog.candidate, { action: 'confirm', fields, share_ack: true }); if (!result.task_id) throw new Error('未创建正式任务，请重新核对。'); return getProjectTask(project.id, result.task_id) } catch (cause) { const latest = await getProjectCandidates(project.id).catch(() => null); if (!latest?.items.some(item => item.id === dialog.candidate.id)) { setItems([]); setSelected(null); onReviewed() }; throw cause } }} onSaved={(task) => { setDialog(null); onReviewed(); onTask(task.id) }} />}
    {dialog && dialog.mode !== 'confirm' && <CandidateReviewDialog project={project} candidate={dialog.candidate} mode={dialog.mode} onClose={() => setDialog(null)} onSaved={(taskId) => { setDialog(null); onReviewed(); setSelected(null); setRetry((value) => value + 1); if (taskId) onTask(taskId) }} />}
  </section>
}

function CandidateEvidenceView({ candidate }: { candidate: TaskCandidate }) {
  return <div className="project-candidate-evidence"><h4>来源依据</h4>{candidate.evidence.map((source) => <details key={source.id}><summary>{source.kind === 'message' ? '聊天记录' : source.kind === 'document' ? '项目文档' : '会议纪要'} · {`${new Date(source.created_at).toISOString().slice(0, 16).replace('T', ' ')} UTC`} · 第 {source.part + 1} 段</summary><pre>{source.text}</pre></details>)}</div>
}

function CandidateReviewDialog({ project, candidate, mode, onClose, onSaved }: { project: ProjectDetail; candidate: TaskCandidate; mode: 'ignore' | 'merge'; onClose: () => void; onSaved: (taskId: string | null) => void }) {
  const [query, setQuery] = useState('')
  const [targets, setTargets] = useState<ProjectTask[]>([])
  const [pending, setPending] = useState(false)
  const [target, setTarget] = useState('')
  const [error, setError] = useState('')
  useEffect(() => {
    if (mode !== 'merge') return
    const controller = new AbortController()
    getProjectTasks(project.id, new URLSearchParams({ q: query, limit: '25' }), controller.signal).then((data) => setTargets(data.items)).catch((cause) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '合并目标读取失败。') })
    return () => controller.abort()
  }, [mode, query, project.id])
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (pending) return
    const data = new FormData(event.currentTarget)
    const selected = targets.find((task) => task.id === target)
    setPending(true); setError('')
    try { const result = await reviewProjectCandidate(project.id, candidate, { action: mode, reason: String(data.get('reason') ?? ''), share_ack: !!data.get('share_ack'), target_task_id: selected?.id, target_version: selected?.version }); onSaved(result.task_id) }
    catch (cause) { setError(cause instanceof Error ? cause.message : '审核失败，请重试。'); setPending(false) }
  }
  return <ProjectDialog title={mode === 'merge' ? '合并候选到正式任务' : '忽略任务建议'} onClose={onClose} busy={pending}><form className="project-form" onSubmit={(event) => void submit(event)}><p>{candidate.fields.title}</p>{mode === 'merge' && <><label htmlFor="merge-search">搜索正式任务</label><Input id="merge-search" disabled={pending} value={query} onChange={(event) => setQuery(event.target.value)} /><label htmlFor="merge-target">合并目标</label><NativeSelect id="merge-target" disabled={pending} value={target} onChange={(event) => setTarget(event.target.value)} required><option value="">请选择已确认任务</option>{targets.map((task) => <option value={task.id} key={task.id}>{task.title}</option>)}</NativeSelect><label className="project-share-ack"><Input name="share_ack" disabled={pending} type="checkbox" required />确认关联结论对组织共享，原文依据按权限读取。</label></>}<label htmlFor="candidate-reason">{mode === 'ignore' ? '忽略原因' : '合并说明'}</label><Textarea id="candidate-reason" disabled={pending} name="reason" required={mode === 'ignore'} maxLength={2000} rows={3} />{error && <p role="alert" className="form-notice error">{error}</p>}<div className="dialog-actions"><Button variant="outline" type="button" disabled={pending} onClick={onClose}>取消</Button><Button type="submit" variant="default" disabled={pending || (mode === 'merge' && !target)}>{pending ? '正在审核…' : mode === 'merge' ? '合并建议' : '忽略建议'}</Button></div></form></ProjectDialog>
}

function candidateStatusFromHash() { const value = new URLSearchParams(window.location.hash.split('?')[1]).get('candidate_status') ?? ''; return ['pending_review', 'confirmed', 'ignored', 'merged', 'superseded'].includes(value) ? value : 'pending_review' }
