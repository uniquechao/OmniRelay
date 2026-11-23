import {useLayoutEffect,useRef} from 'react'
import {bindTaskMarkdown} from './task-markdown.js'

type Props = {projectId:string; name:'description'|'acceptance'; label:string; value:string; readOnly:boolean; pending:boolean; files:Map<string,File>; onInput:(value:string)=>void}
export function TaskMarkdownField({projectId,name,label,value,readOnly,pending,files,onInput}:Props) {
  const ref=useRef<HTMLFieldSetElement>(null)
  const change=useRef(onInput)
  change.current=onInput
  useLayoutEffect(()=>bindTaskMarkdown(ref.current!,projectId,files,(v:string)=>change.current(v)),[projectId,files])
  return <fieldset ref={ref} className="task-md" disabled={pending} data-md-label={label}>
    <legend>{label}</legend>
    <div className="task-md-actions" hidden={readOnly}>
      <button type="button" data-md-toggle>编辑</button>
      <button type="button" data-md-attach aria-label={'添加附件到'+label}>添加附件</button>
      <input type="file" multiple hidden />
    </div>
    <div data-md-preview tabIndex={readOnly ? undefined : 0} aria-label={label+'预览'} />
    <textarea name={name} aria-label={name==='description'?'任务正文':label} readOnly={readOnly} defaultValue={value} maxLength={name==='description'?10000:4000} rows={5} hidden placeholder="输入 Markdown，可粘贴图片或文件" />
    <small data-md-status role="status" />
  </fieldset>
}
