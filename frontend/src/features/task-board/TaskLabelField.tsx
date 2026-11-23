import {useRef, useState} from 'react'
import {Button, Dialog, DialogTrigger, Popover} from 'react-aria-components'
import {Tag} from 'lucide-react'
import {ApiError, createBoardTag, getProjectBoard, type TaskTag} from './api'

export function TaskLabelField({projectId,tags,value,onChange,onTags,disabled,canCreate}:{
  projectId:string; tags:TaskTag[]; value:string[]; onChange:(ids:string[])=>void; onTags:(tags:TaskTag[])=>void; disabled:boolean; canCreate:boolean
}) {
  const [open,setOpen]=useState(false)
  const search=useRef<HTMLInputElement>(null)
  const [query,setQuery]=useState('')
  const [pending,setPending]=useState(false)
  const [error,setError]=useState('')
  const lock=useRef(false)
  const name=query.trim()
  const matches=tags.filter(tag=>(tag.type+' '+tag.name).toLocaleLowerCase().includes(name.toLocaleLowerCase()))
  async function create() {
    if (!name || !canCreate || lock.current) return
    lock.current=true;setPending(true);setError('')
    try {
      const board=await getProjectBoard(projectId,new URLSearchParams({limit:'1'}))
      let tag=board.tags.find(tag=>tag.name===name)
      let next=board.tags
      if (!tag) {
        const id=crypto.randomUUID()
        const result=await createBoardTag(projectId,{id,name,type:'项目',color:'#64748b',expected_version:board.version,request_key:crypto.randomUUID()})
        next=result.tags
        tag=next.find(item=>item.id===id)
      }
      if (!tag) throw new Error()
      onTags(next); onChange([...new Set([...value,tag.id])]);setQuery('')
    } catch (cause) {setError(cause instanceof ApiError && cause.status===409 ? '标签已更新，请重试。' : '创建标签失败，请重试。')}
    finally {lock.current=false;setPending(false);requestAnimationFrame(()=>search.current?.focus())}
  }
  return <DialogTrigger isOpen={open} onOpenChange={next=>{setOpen(next);if(next){setQuery('');setError('')}}}>
    <Button className="participants-trigger task-label-trigger" aria-label="添加或移除标签" isDisabled={disabled}><Tag size={14} aria-hidden/>添加标签</Button>
    <Popover className="task-label-popover prototype-picker" placement="bottom end" offset={4}>
      <Dialog className="task-label-dialog" aria-label="添加或移除标签"><input ref={search} autoFocus type="search" aria-label="搜索标签" placeholder="搜索标签" maxLength={60} value={query} disabled={pending} onChange={event=>setQuery(event.target.value)}/>
        <div className="task-label-options">{matches.map(tag=><label key={tag.id}><input type="checkbox" checked={value.includes(tag.id)} disabled={pending||disabled} onChange={event=>onChange(event.target.checked ? [...value,tag.id] : value.filter(id=>id!==tag.id))}/><span>{tag.name}</span></label>)}
        {!matches.length && <p>{tags.length ? '没有匹配标签' : '暂无项目标签'}</p>}</div>
        {canCreate && !tags.some(tag=>tag.name===name) && name && <Button isDisabled={pending} onPress={()=>void create()}>创建标签「{name}」</Button>}
        {error && <p role="alert">{error}</p>}
        <Button onPress={()=>setOpen(false)}>完成</Button>
      </Dialog>
    </Popover>
  </DialogTrigger>
}
