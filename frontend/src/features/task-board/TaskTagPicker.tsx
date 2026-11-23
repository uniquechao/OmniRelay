import { useId, useState } from 'react'
import { ChevronDown, Tag } from 'lucide-react'
import { Button } from './controls'
import { Input } from './controls'
import { Popover, PopoverContent, PopoverTrigger } from './controls'
import type { TaskTag } from './api'

export function TaskTagPicker({ tags, value, onChange, label, placeholder = '全部标签', name, disabled = false, editor = false }: { tags: TaskTag[]; value: string[]; onChange: (value: string[]) => void; label: string; placeholder?: string; name?: string; disabled?: boolean; editor?: boolean }) {
  const id = useId()
  const [query, setQuery] = useState('')
  const matches = tags.filter(tag => `${tag.type} ${tag.name}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
  return <div className={`task-tag-picker${editor ? ' task-tag-editor' : ''}`}>
    {name && value.map(key => <input type="hidden" key={key} name={name} value={key} disabled={disabled} />)}
    <Popover onOpenChange={open => { if (open) setQuery('') }}>
      <PopoverTrigger asChild><Button type="button" variant={editor ? 'ghost' : 'outline'} disabled={disabled} aria-label={editor ? label : undefined}>{editor && <Tag size={14} aria-hidden />}{!editor && value.length ? `已选 ${value.length} 个标签` : placeholder}{!editor && <ChevronDown size={14} aria-hidden />}</Button></PopoverTrigger>
      <PopoverContent className="task-tag-picker-panel" align="start" aria-label={label}>
        <label htmlFor={id} className="sr-only">搜索标签或类型</label><Input id={id} type="search" value={query} placeholder="搜索标签或类型" onChange={event => setQuery(event.target.value)} />
        {Array.from(new Set(matches.map(tag => tag.type))).map(type => <fieldset key={type}><legend>{type}</legend>{matches.filter(tag => tag.type === type).map(tag => <label key={tag.id}><input type="checkbox" checked={value.includes(tag.id)} disabled={disabled} onChange={event => onChange(event.target.checked ? [...value, tag.id] : value.filter(key => key !== tag.id))} /><span>{tag.name}</span></label>)}</fieldset>)}
        {!matches.length && <p>{query ? '没有匹配标签' : '暂无标签'}</p>}
      </PopoverContent>
    </Popover>
  </div>
}
