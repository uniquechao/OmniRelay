import {useRef, useState} from 'react'
import {ProjectDialog} from './ProjectDialog'
import {Button, NativeSelect} from './controls'
import {updateTaskPriorities, type ProjectTask} from './api'

export function TaskBulkEdit({projectId,tasks,onClose,onSaved}:{projectId:string;tasks:ProjectTask[];onClose:()=>void;onSaved:()=>void}) {
  const [selected,setSelected]=useState<Map<string,number>>(new Map())
  const [priority,setPriority]=useState<ProjectTask['priority']>('P2')
  const [pending,setPending]=useState(false)
  const [error,setError]=useState('')
  const key=useRef(crypto.randomUUID())
  async function save() {
    if (pending || !selected.size) return
    setPending(true)
    try {
      await updateTaskPriorities(projectId,Array.from(selected,([id,expected_version])=>({id,expected_version,priority})),key.current)
      onSaved()
    } catch { setError('批量修改未确认。可重试，或关闭后刷新任务再选择。') }
    finally {setPending(false)}
  }
  return <ProjectDialog title="批量操作" onClose={onClose} busy={pending}>
    <div className="task-bulk-form"><fieldset disabled={pending}><legend>选择任务（已选 {selected.size} / 100）</legend>
      {tasks.map(task=><label key={task.id}><input type="checkbox" checked={selected.has(task.id)} disabled={!selected.has(task.id)&&selected.size>=100} onChange={event=>{
        const next=new Map(selected)
        if (event.target.checked) next.set(task.id,task.version)
        else next.delete(task.id)
        setSelected(next); key.current=crypto.randomUUID()
      }}/>T-{String(task.display_number).padStart(3,'0')} {task.title}</label>)}
    </fieldset><label>优先级 <NativeSelect aria-label="优先级" value={priority??''} disabled={pending} onChange={event=>{setPriority(event.target.value as ProjectTask['priority'] || null);key.current=crypto.randomUUID()}}><option value="">未设置</option>{['P0','P1','P2','P3'].map(p=><option key={p}>{p}</option>)}</NativeSelect></label>
    {error&&<p role="alert">{error}</p>}<Button disabled={pending||!selected.size} onClick={()=>void save()}>应用到所选任务</Button></div>
  </ProjectDialog>
}
