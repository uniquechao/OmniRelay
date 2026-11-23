import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { ArrowUp, ArrowDown, Plus, X } from 'lucide-react'
import { Modal } from './controls'
import { Button } from './controls'
import { Input } from './controls'
import { Textarea } from './controls'
import { NativeSelect } from './controls'
import {
  getProjectBoard,
  saveProjectBoard,
  type BoardColumn,
  type BoardStage,
  type ProjectBoardPage,
  type TaskTag,
} from './api'

const stages = { todo: '待开始', in_progress: '进行中', blocked: '受阻', done: '已完成' }
export function ProjectBoardSettings({
  projectId,
  board,
  onClose,
  onSaved,
  onDenied,
  initialColumnId,
}: {
  projectId: string
  board: ProjectBoardPage
  onClose: () => void
  onSaved: () => void
  onDenied: (cause: unknown) => void
  initialColumnId?: string
}) {
  const id = useId()
  const [baseline, setBaseline] = useState(board)
  const [columns, setColumns] = useState<BoardColumn[]>(() =>
    board.columns.map(({ id, name, color, stage }) => ({ id, name, color, stage }))
  )
  const [draftTags, setDraftTags] = useState<TaskTag[]>(() => board.tags.map(tag => ({...tag})))
  const [defaultId, setDefaultId] = useState(board.default_column_id)
  const [migrations, setMigrations] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const [closing, setClosing] = useState(false)
  const [conflict, setConflict] = useState(false)
  const formRef = useRef<HTMLFormElement>(null)
  const initialFocusDone = useRef(false)
  const focusField = (label: string) => requestAnimationFrame(() => formRef.current?.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)?.focus())
  useEffect(() => {
    if (!initialColumnId || initialFocusDone.current) return
    const index = board.columns.findIndex(column => column.id === initialColumnId)
    if (index < 0) return
    const frame = requestAnimationFrame(() => {
      initialFocusDone.current = true
      formRef.current?.querySelector<HTMLInputElement>(`input[aria-label="第${index + 1}列名称"]`)?.focus()
    })
    return () => cancelAnimationFrame(frame)
  }, [initialColumnId, board.columns])
  const key = useRef(crypto.randomUUID())
  const keepEditing = useRef<HTMLButtonElement>(null)
  const returnFocus = useRef<HTMLElement | null>(null)
  const promptOpen = useRef(false)
  const snapshot = (items: BoardColumn[], tags: TaskTag[], defaultColumn: string) => JSON.stringify([
    items.map(({id,name,color,stage}) => ({id,name,color,stage})),
    tags.map(({id,type,name,color}) => ({id,type,name,color})), defaultColumn,
  ])
  const dirty = snapshot(columns, draftTags, defaultId) !== snapshot(baseline.columns, baseline.tags, baseline.default_column_id)
  const guard = useRef(dirty)
  useEffect(() => {
    guard.current = dirty
  }, [dirty])
  const close = useCallback(() => {
    if (guard.current) {
      if (!promptOpen.current) returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      promptOpen.current = true
      setClosing(true)
    } else onClose()
  }, [onClose])
  useEffect(() => { if (closing) keepEditing.current?.focus({ preventScroll: true }) }, [closing])
  useEffect(() => {
    const protect = (event: Event) => {
      if (guard.current) {
        event.preventDefault()
        close()
      }
    }
    const unload = (event: BeforeUnloadEvent) => {
      if (guard.current) event.preventDefault()
    }
    window.addEventListener('omni-grid:before-route-change', protect)
    window.addEventListener('beforeunload', unload)
    return () => {
      window.removeEventListener('omni-grid:before-route-change', protect)
      window.removeEventListener('beforeunload', unload)
    }
  }, [close])
  function change(index: number, patch: Partial<BoardColumn>) {
    setColumns((items) => items.map((item, i) => (i === index ? { ...item, ...patch } : item)))
  }
  return (
    <Modal
      className="project-dialog board-settings-drawer"
      presentation="sheet"
      returnTo={initialColumnId ? document.getElementById('board-menu-'+initialColumnId) : undefined}
      labelledBy={id}
      busy={pending}
      onClose={close}
    >
      <header>
        <span>任务看板</span>
        <Button type="button" variant="ghost" disabled={pending} onClick={close} aria-label="关闭看板设置"><X size={16} aria-hidden /></Button>
      </header>
      {closing && <div className="task-inspector-discard" role="alert">
        <span>有未保存的更改</span>
        <div className="task-discard-actions">
          <Button ref={keepEditing} type="button" variant="outline" disabled={pending} onClick={() => {
            if (pending) return
            promptOpen.current = false; setClosing(false)
            requestAnimationFrame(() => { if (returnFocus.current?.isConnected) returnFocus.current.focus({ preventScroll: true }) })
          }}>继续编辑</Button>
          <Button type="button" variant="outline" disabled={pending} onClick={() => { if (pending) return; guard.current = false; onClose() }}>放弃更改</Button>
        </div>
      </div>}
      <form
        ref={formRef}
        className="project-form board-settings-form"
        onChange={() => {
          key.current = crypto.randomUUID()
        }}
        onSubmit={async (event) => {
          event.preventDefault()
          if (pending || conflict) return
          if (!dirty) { guard.current = false; onClose(); return }
          const data = new FormData(event.currentTarget)
          setPending(true)
          setError('')
          try {
            await saveProjectBoard(projectId, {
              expected_version: baseline.version,
              columns,
              tags: draftTags.map(({id,type,name,color}) => ({id,type,name,color})),
              default_column_id: defaultId,
              migrations,
              blocked_reason: String(data.get('blocked_reason') ?? ''),
              change_reason: String(data.get('change_reason') ?? ''),
              request_key: key.current,
            })
            guard.current = false
            onSaved()
          } catch (cause) {
            if (
              cause &&
              typeof cause === 'object' &&
              'status' in cause &&
              [401, 403, 404].includes(Number(cause.status))
            ) {
              onDenied(cause)
              return
            }
            setError(cause instanceof Error ? cause.message : '看板设置保存失败。')
            if (cause && typeof cause === 'object' && 'status' in cause && cause.status === 409)
              setConflict(true)
          } finally {
            setPending(false)
          }
        }}
      >
        <h2 id={id}>看板设置</h2>
        <fieldset disabled={pending || conflict} className="board-settings-fields">
          <section className="board-settings-section" aria-label="状态列">
            <div className="board-settings-section-heading"><h3>状态列</h3><Button type="button" variant="outline" disabled={columns.length >= 30} onClick={() => {setColumns(items => [...items,{id:crypto.randomUUID(),name:'',color:'#798294',stage:'todo'}]);focusField(`第${columns.length+1}列名称`);key.current=crypto.randomUUID()}}><Plus size={14} aria-hidden />添加状态</Button></div>
            <div className="board-settings-grid board-settings-head"><span>状态名称</span><span>标准阶段</span><span>颜色</span><span>顺序</span></div>
            <ol className="board-settings-list">{columns.map((column,index) => <li className="board-settings-grid" key={column.id} data-column-id={column.id}>
              <Input aria-label={`第${index+1}列名称`} value={column.name} maxLength={100} required onChange={event => change(index,{name:event.target.value})} />
              <NativeSelect aria-label={`第${index+1}列标准阶段`} value={column.stage} onChange={event => change(index,{stage:event.target.value as BoardStage})}>{Object.entries(stages).map(([value,label]) => <option value={value} key={value}>{label}</option>)}</NativeSelect>
              <NativeSelect aria-label={`第${index+1}列状态颜色`} value={column.color} style={{color:column.color}} onChange={event => change(index,{color:event.target.value})}>{Object.entries({'#798294':'灰色','#1746e8':'蓝色','#b66a18':'橙色','#67ac0b':'绿色','#7653b6':'紫色'}).map(([value,label]) => <option value={value} key={value}>{label}</option>)}{!['#798294','#1746e8','#b66a18','#67ac0b','#7653b6'].includes(column.color) && <option value={column.color}>自定义</option>}</NativeSelect>
              <div className="board-settings-actions"><Button type="button" variant="outline" aria-label={`上移「${column.name}」`} title={`上移「${column.name}」`} disabled={!index} onClick={() => {setColumns(items => {const copy=[...items];[copy[index-1],copy[index]]=[copy[index],copy[index-1]];return copy});key.current=crypto.randomUUID()}}><ArrowUp size={14} aria-hidden /></Button><Button type="button" variant="outline" aria-label={`下移「${column.name}」`} title={`下移「${column.name}」`} disabled={index===columns.length-1} onClick={() => {setColumns(items => {const copy=[...items];[copy[index+1],copy[index]]=[copy[index],copy[index+1]];return copy});key.current=crypto.randomUUID()}}><ArrowDown size={14} aria-hidden /></Button><Button type="button" variant="outline" aria-label={`删除${column.name}列`} title={`移除「${column.name}」`} disabled={columns.length===1} onClick={() => {const remaining=columns.filter(item=>item.id!==column.id);setColumns(remaining);if(defaultId===column.id)setDefaultId(remaining[0].id);setMigrations(values=>Object.fromEntries(Object.entries(values).map(([source,target])=>[source,target===column.id?'':target])));key.current=crypto.randomUUID()}}><X size={14} aria-hidden /></Button></div>
            </li>)}</ol>
            <label className="board-default-column">新任务默认状态<NativeSelect aria-label="默认列" value={defaultId} required onChange={event => setDefaultId(event.target.value)}>{!columns.some(column => column.id===defaultId) && <option value="">请选择保留的默认列</option>}{columns.map(column => <option value={column.id} key={column.id}>{column.name || '新状态'}</option>)}</NativeSelect></label>
            {baseline.columns.some(old=>!columns.some(column=>column.id===old.id)) && <div className="board-settings-migrations"><h3>任务迁移</h3><p>迁移该列全部任务，包括未加载和筛选隐藏的任务。</p>{baseline.columns.filter(old=>!columns.some(column=>column.id===old.id)).map(old=><label key={old.id}>{old.name}的全部任务迁移至<NativeSelect aria-label={`${old.name}的迁移目标`} required value={migrations[old.id]??''} onChange={event=>setMigrations(values=>({...values,[old.id]:event.target.value}))}><option value="">选择目标状态</option>{columns.map(column=><option key={column.id} value={column.id}>{column.name || '新状态'}</option>)}</NativeSelect></label>)}</div>}
            {columns.some(column=>column.stage==='blocked' && baseline.columns.find(old=>old.id===column.id)?.stage!=='blocked') || Object.values(migrations).some(target=>columns.find(column=>column.id===target)?.stage==='blocked') ? <label>迁入受阻列的原因<Textarea name="blocked_reason" maxLength={4000} /></label> : <input type="hidden" name="blocked_reason" value="" />}
            {baseline.columns.some(old=>old.stage==='done' && (columns.find(column=>column.id===old.id)?.stage ?? columns.find(column=>column.id===migrations[old.id])?.stage)!=='done') && <label>重新打开已完成任务的原因<Textarea name="change_reason" maxLength={4000} /></label>}
          </section>
          <section className="board-settings-section" aria-label="项目标签">
            <div className="board-settings-section-heading"><h3>项目标签</h3><Button type="button" variant="outline" disabled={draftTags.length>=200} onClick={()=>{setDraftTags(items=>[...items,{id:crypto.randomUUID(),project_id:projectId,type:'',name:'',color:'#555b6b',usage_count:0}]);focusField(`第${draftTags.length+1}个标签的类型`);key.current=crypto.randomUUID()}}><Plus size={14} aria-hidden />添加标签</Button></div>
            <div className="board-settings-tag-grid board-settings-head"><span>类型</span><span>标签名称</span><span /></div>
            <ul className="board-settings-list">{draftTags.map((tag,index)=><li className="board-settings-tag-grid" key={tag.id}><Input aria-label={`第${index+1}个标签的类型`} value={tag.type} maxLength={50} required onChange={event=>{setDraftTags(items=>items.map(item=>item.id===tag.id?{...item,type:event.target.value}:item))}} /><Input aria-label={`第${index+1}个标签的名称`} value={tag.name} maxLength={100} required onChange={event=>{setDraftTags(items=>items.map(item=>item.id===tag.id?{...item,name:event.target.value}:item))}} /><Button type="button" variant="outline" aria-label={`移除标签「${tag.name}」`} title={tag.usage_count===undefined?'标签引用尚未确认':tag.usage_count>0?'任务正在使用此标签':`移除标签「${tag.name}」`} disabled={tag.usage_count===undefined || tag.usage_count>0} onClick={()=>{setDraftTags(items=>items.filter(item=>item.id!==tag.id));key.current=crypto.randomUUID()}}><X size={14} aria-hidden /></Button></li>)}</ul>
          </section>
          <details className="board-settings-advanced"><summary>自定义颜色</summary>{columns.map((column,index)=><label key={column.id}>{column.name || `第${index+1}列`}<Input type="color" aria-label={`第${index+1}列自定义颜色`} value={column.color} onChange={event=>change(index,{color:event.target.value})} /></label>)}</details>
        </fieldset>
        {error && <p role="alert">{error}</p>}
        {conflict && (
          <Button
            type="button"
            variant="outline"
            onClick={async () => {
              try {
                const current = await getProjectBoard(projectId)
                setBaseline(current)
                setError(
                  `已读取看板版本 ${current.version}。请核对当前列：${current.columns.map((column) => column.name).join('、')}，草稿保持不变。`
                )
                key.current = crypto.randomUUID()
              } catch (cause) {
                setError(cause instanceof Error ? cause.message : '读取失败。')
              }
            }}
          >
            读取当前版本
          </Button>
        )}
        {conflict && baseline.version !== board.version && (
          <Button type="button" variant="outline" onClick={() => setConflict(false)}>
            已核对新版本，保留草稿继续
          </Button>
        )}
        <div className="dialog-actions">
          <Button type="button" variant="outline" disabled={pending} onClick={close}>
            取消
          </Button>
          <Button
            type="submit"
            disabled={pending || conflict || !columns.some((column) => column.id === defaultId)}
          >
            {pending ? '正在保存…' : '保存看板设置'}
          </Button>
        </div>
      </form>
    </Modal>
  )
}
