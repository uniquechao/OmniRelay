import {createContext, useContext, useEffect, useMemo, useRef, useState} from 'react'
import {Pencil} from 'lucide-react'
import {DataList, type Column, type RemotePage} from '../../ui/components/Widgets'
import {ApiError, claimProjectTask, getProjectTasks, updateProjectTask, type ProjectBoardPage, type ProjectDetail, type ProjectTask, type TaskStatus} from './api'
import {Button, Popover, PopoverAnchor, PopoverContent} from './controls'
import {TaskLabelOptions} from './TaskLabelField'
import {taskDate, taskDateInstant} from './task-dates'

type Field = 'start_at' | 'due_at' | 'tag_ids' | 'status'
type Edit = {task:ProjectTask; field:Field; anchor:HTMLElement; column?:string; status?:TaskStatus}
type Patch = Partial<Pick<ProjectTask,'status'|'start_at'|'due_at'|'tag_ids'|'blocked_reason'>> & {column_id?:string;change_reason?:string}
type Props = {
  project: ProjectDetail
  board: ProjectBoardPage
  canManage: boolean
  query: string
  showDates: boolean
  showTags: boolean
  revision: number
  onPage: (tasks:ProjectTask[])=>void
  onRefresh: ()=>void
  onOpen: (id:string)=>void
  onNotice: (message:string)=>void
}
const empty:ProjectTask[]=[]
const rowName=(row:ProjectTask)=>row.title
const defaultSort={key:'',direction:'asc'} as const
const cancelled='__cancelled__'
const priorities={P0:'紧急',P1:'高',P2:'中',P3:'低'}
const editorId=(task:ProjectTask, field:Field)=>'task-list-'+field+'-'+task.id
const TaskRowContext=createContext<(Pick<Props,'project'|'board'|'canManage'|'onOpen'> & {
  busy:boolean; edit:(entry:Edit)=>void; commit:(task:ProjectTask,patch:Patch|'claim')=>Promise<void>
}) | null>(null)

function TaskCell({task,field}:{task:ProjectTask;field:string}) {
  const {project,board,canManage,busy,edit,commit,onOpen}=useContext(TaskRowContext)!
  const zone=project.business_timezone || 'UTC'
  const number=task.task_code
  const active=task.status!=='done' && task.status!=='cancelled'
  const member=project.current_member_id
  const canWork=Boolean(member && project.actions.includes('update_own_task'))
  switch(field) {
    case 'title':
      return <button className="task-list-title" id={'board-task-'+task.id} data-view-trigger={task.id} aria-haspopup="dialog"
        aria-label={number+' '+task.title} onClick={()=>onOpen(task.id)}><small>{number}</small><strong>{task.title}</strong></button>
    case 'owner':
      return task.owner_member_id ? project.members.find(m=>m.id===task.owner_member_id)?.display_name || task.relation_names?.[task.owner_member_id] || '成员姓名未知' : '未分配'
    case 'priority':
      return task.priority ? <span className="board-tags"><span className="board-priority" data-priority={task.priority}>{priorities[task.priority]}</span></span> : '—'
    case 'status': {
      const current=board.columns.find(c=>c.id===task.column_id)
      const value=task.status==='cancelled'?cancelled:task.column_id
      const allowed=canManage || (canWork && active && (task.owner_member_id===member || task.participant_ids?.includes(member!)))
      if(!allowed)return <span>{task.status==='cancelled'?'已取消':current?.name || '—'}</span>
      return <select className="select task-list-status" id={editorId(task,'status')} aria-label={'修改状态 '+task.title} value={value}
        disabled={busy} onChange={e=>{
          const target=board.columns.find(c=>c.id===e.target.value)
          const status=target?.stage ?? 'cancelled'
          if(status==='cancelled' || (status!==task.status && !active) || (status==='blocked' && !task.blocked_reason?.trim())) {
            edit({task,field:'status',column:target?.id,status,anchor:e.currentTarget})
          } else {
            void commit(task,{status,column_id:target?.id,blocked_reason:status==='blocked'?task.blocked_reason:''})
          }
        }}>{board.columns.map(c=><option key={c.id} value={c.id}>{c.name}</option>)}{canManage && <option value={cancelled}>已取消</option>}</select>
    }
    case 'start_at':
    case 'due_at': {
      const value=task[field], name=field==='start_at'?'开始日期':'截止日期'
      const date=value?<time dateTime={value} className={field==='due_at' && task.overdue?'board-overdue':undefined}>{taskDate(value,zone)}</time>:'—'
      return canManage ? <button id={editorId(task,field)} className="task-list-property" aria-label={'修改'+name+' '+task.title} aria-haspopup="dialog"
        disabled={busy} onClick={e=>edit({task,field,anchor:e.currentTarget})}>{date}<Pencil size={12} aria-hidden/></button> : date
    }
    case 'tag_ids': {
      const tags=task.tag_ids?.map(id=>board.tags.find(t=>t.id===id)?.name || task.relation_names?.[id] || '未知标签') || []
      const labels=tags.length?<span className="task-list-tags">{tags.map((name,i)=><span key={task.tag_ids![i]}>{name}</span>)}</span>:canManage?'添加标签':'—'
      return canManage ? <button className="task-list-property" id={editorId(task,'tag_ids')} aria-label={'修改标签 '+task.title} aria-haspopup="dialog"
        disabled={busy} onClick={e=>edit({task,field:'tag_ids',anchor:e.currentTarget})}>{labels}{tags.length>0 && <Pencil size={12} aria-hidden/>}</button> : labels
    }
    case 'actions':
      return <span className="dev-actions">{!task.owner_member_id && active && member && (canManage || canWork) &&
        <button className="row-action" aria-label={'认领 '+task.title} disabled={busy} onClick={()=>void commit(task,'claim')}>认领</button>}
        <button id={'task-view-'+task.id} className="row-action" aria-label={'查看 '+task.title} aria-haspopup="dialog" onClick={()=>onOpen(task.id)}>查看</button></span>
  }
}

function RowEditor({entry,project,board,busy,error,close,commit}:{entry:Edit;project:ProjectDetail;board:ProjectBoardPage;busy:boolean;error:string;
  close:()=>void;commit:(task:ProjectTask,patch:Patch)=>Promise<void>}) {
  const {task,field}=entry,zone=project.business_timezone || 'UTC'
  const [start,setStart]=useState(()=>taskDate(task.start_at,zone)),[due,setDue]=useState(()=>taskDate(task.due_at,zone))
  const [tags,setTags]=useState(board.tags),[selected,setSelected]=useState(task.tag_ids || [])
  const [block,setBlock]=useState(task.blocked_reason || ''),[reason,setReason]=useState(''),[invalid,setInvalid]=useState('')
  const anchor=()=>document.getElementById(editorId(task,field)) || entry.anchor
  const virtualRef=useMemo(()=>({current:{getBoundingClientRect:()=>anchor().getBoundingClientRect()}}),[entry])
  const title=field==='tag_ids'?'编辑标签':field==='status'?'修改状态':'修改日期'
  function save() {
    if(field==='tag_ids') {void commit(task,{tag_ids:selected});return}
    if(field==='status') {
      void commit(task,{status:entry.status,column_id:entry.column,blocked_reason:entry.status==='blocked'?block.trim():'',change_reason:reason.trim()})
      return
    }
    try {
      if(start && due && start>due)throw new Error('截止日期不能早于开始日期。')
      const instant=(value:string,original:string|null|undefined,end=false)=>value===taskDate(original,zone)?original ?? null:value?taskDateInstant(value,zone,end):null
      void commit(task,{start_at:instant(start,task.start_at),due_at:instant(due,task.due_at,true)})
    } catch(cause) {setInvalid(cause instanceof Error?cause.message:'日期无效。')}
  }
  return <Popover open onOpenChange={open=>{if(!open && !busy)close()}}>
    <PopoverAnchor virtualRef={virtualRef}/>
    <PopoverContent className="task-list-editor" align="start" aria-label={title+' '+task.title}
      onCloseAutoFocus={e=>{e.preventDefault();anchor()?.focus({preventScroll:true})}}
      onOpenAutoFocus={e=>{const el=document.getElementById('task-list-editor-'+field);if(el){e.preventDefault();el.focus()}}}>
      <strong>{title}</strong>
      {field==='tag_ids'?<div className="task-label-dialog"><TaskLabelOptions projectId={project.id} tags={tags} value={selected} onTags={setTags} onChange={setSelected}
        disabled={busy} canCreate={project.actions.includes('configure_board')} doneLabel="保存标签" onDone={save}/></div>:
        <form onSubmit={e=>{e.preventDefault();save()}}>
          {field==='status'?<>
            {entry.status==='blocked' && <label>受阻原因<textarea required maxLength={4000} value={block} onChange={e=>setBlock(e.target.value)} disabled={busy}/></label>}
            {(entry.status==='cancelled' || task.status==='done' || task.status==='cancelled') &&
              <label>{entry.status==='cancelled'?'取消原因':'重新打开原因'}<textarea required maxLength={4000} value={reason} onChange={e=>setReason(e.target.value)} disabled={busy}/></label>}
          </>:<>
            <label>开始日期<input id="task-list-editor-start_at" type="date" value={start} max={due || undefined} onChange={e=>setStart(e.target.value)} disabled={busy}/></label>
            <label>截止日期<input id="task-list-editor-due_at" type="date" value={due} min={start || undefined} onChange={e=>setDue(e.target.value)} disabled={busy}/></label>
          </>}
          <div className="dialog-actions"><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button>
            <Button type="submit" disabled={busy}>{field==='status'?'保存状态':'保存日期'}</Button></div>
        </form>}
      {(invalid || error) && <p role="alert">{invalid || error}</p>}
    </PopoverContent></Popover>
}

export function ProjectTaskList(props:Props) {
  const {project,board,query,revision,onPage,showDates,showTags}=props
  const [editor,setEditor]=useState<Edit|null>(null)
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const lock=useRef(false)
  const alive=useRef(true)
  const attempt=useRef({signature:'',key:''})
  const restoreFocus=useRef('')
  useEffect(()=>{alive.current=true;return ()=>{alive.current=false}},[])
  const columns=useMemo<Column<ProjectTask>[]>(()=>{
    const fields=[['title','任务'],['status','状态'],['priority','优先级'],['owner','负责人']]
    if(showDates)fields.push(['start_at','开始日期'],['due_at','截止日期'])
    if(showTags)fields.push(['tag_ids','标签'])
    fields.push(['actions','操作'])
    return fields.map(([key,label])=>({key,label,render:task=><TaskCell field={key} task={task}/>}))
  },[showDates,showTags])
  const load=useMemo<RemotePage<ProjectTask>>(()=>async ({page,pageSize},signal)=>{
    const params=new URLSearchParams(query)
    params.set('limit',String(pageSize))
    params.set('page',String(page+1))
    onPage([])
    const result=await getProjectTasks(project.id,params,signal)
    signal.throwIfAborted()
    if(typeof result.total!=='number')throw new Error('任务列表缺少总数。')
    onPage(result.items)
    const focusId=restoreFocus.current
    if (focusId) {
      restoreFocus.current=''
      requestAnimationFrame(()=>{
        if (alive.current && document.activeElement===document.body) {
          const target=document.getElementById(focusId) ?? document.querySelector<HTMLElement>('.task-list-layout .list-scroll')
          target?.focus({preventScroll:true})
        }
      })
    }
    return {items:result.items,total:result.total}
  },[project.id,query,onPage])
  async function commit(task:ProjectTask,patch:Patch|'claim') {
    if(lock.current)return
    lock.current=true
    setBusy(true)
    setError('')
    const signature=JSON.stringify([task.id,task.version,patch])
    if(signature!==attempt.current.signature)attempt.current={signature,key:crypto.randomUUID()}
    const key=attempt.current.key
    try {
      if(patch==='claim') {
        await claimProjectTask(project.id,task,key)
      } else {
        const {title,description,acceptance,owner_member_id,participant_ids,priority,start_at,due_at,
          tag_ids,status,blocked_reason,completion_note}=task
        await updateProjectTask(project.id,task,{
          title,description,acceptance,owner_member_id,participant_ids,priority,start_at,due_at,
          tag_ids,status,blocked_reason,completion_note,change_reason:'',...patch,request_key:key,
        })
      }
      if (!alive.current) return
      restoreFocus.current=patch==='claim' ? 'task-view-'+task.id : editorId(task,editor?.field ?? 'status')
      setEditor(null)
      props.onNotice(patch==='claim'?'任务已认领。':'任务已更新。')
      props.onRefresh()
    } catch(cause) {
      if(!alive.current)return
      if(cause instanceof ApiError && [403,404,409].includes(cause.status)){
        setEditor(null)
        setError(cause.status===409?'任务或看板已有变更，请核对最新内容后重试。':'当前账号已无法执行此操作。')
        props.onRefresh()
      } else setError(cause instanceof Error?cause.message:'保存失败，请重试。')
    } finally {lock.current=false;if(alive.current)setBusy(false)}
  }
  return <TaskRowContext.Provider value={{project,board,canManage:props.canManage,busy,edit:entry=>{setError('');setEditor(entry)},commit,onOpen:props.onOpen}}>
    {error && !editor && <p className="board-notice error" role="alert">{error}</p>}
    <DataList label="任务列表" itemLabel="任务" rows={empty} rowLabel={rowName} search={rowName} columns={columns} toolbar={false} selection={false} defaultSort={defaultSort} loadPage={load} revision={revision}/>
    {editor && <RowEditor key={editor.task.id+editor.field} entry={editor} {...{project,board,busy,error,commit}} close={()=>{setEditor(null);setError('')}}/>}
  </TaskRowContext.Provider>
}
