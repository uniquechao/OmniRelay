import {useId, useLayoutEffect, useRef, useState, type ReactNode} from 'react';
import {createPortal} from 'react-dom';
import {Button, Icon} from './Elements';
import {ui, useWidget, useLatest, type DialogOptions, type Choice, type Department, type Selection, type Person} from './runtime';

type DialogProps = Omit<DialogOptions,'body'> & {children: ReactNode; confirmDisabled?: boolean};
export function Dialog({children,title,description,variant,tone,confirm,drawer,confirmDisabled=false,...handlers}: DialogProps) {
  const latest = useLatest(handlers);
  const [host,setHost] = useState<Element | null>(null);
  useLayoutEffect(()=>{
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    let active=true;
    const dialog=ui.openDialog({title,description,variant,tone,confirm,drawer,body:'<div data-react-body></div>',
      isDirty:form=>latest.current.isDirty?.(form) ?? false,
      onSubmit:form=>latest.current.onSubmit?.(form),
      onClose:confirmed=>{ if(active) latest.current.onClose?.(confirmed); },
      restoreFocus:()=>active?(latest.current.restoreFocus?.() ?? trigger):null});
    setHost(dialog.querySelector('[data-react-body]'));
    return ()=>{active=false; dialog.close(); dialog.remove();};
  },[title,description,variant,tone,confirm,drawer,latest]);
  useLayoutEffect(()=>{
    const button=host?.closest('dialog')?.querySelector<HTMLButtonElement>('button[type="submit"]');
    if(button) button.disabled=confirmDisabled;
  },[host,confirmDisabled]);
  useLayoutEffect(()=>{
    if(variant==='form') host?.querySelector<HTMLElement>('input:not([type="hidden"]),textarea,[aria-pressed="true"],.tree-choice')?.focus();
  },[host,variant]);
  return host ? createPortal(children, host) : null;
}
export function Drawer(props: DialogProps & {width?:'normal'|'wide'}) {
  const {width='normal',...rest}=props;
  return <Dialog {...rest} drawer={width}/>;
}
export function SearchSelect({name,label,choices,multiple=false,initial,onChange}: {
  name:string;label:string;choices:Choice[];multiple?:boolean;initial?:string[];onChange?:(ids:string[])=>void;
}) {
  const id=useId();
  const latest=useLatest(onChange);
  const ref=useWidget((host,signal)=>{
    host.innerHTML=ui.combo(ui.escape(id),ui.escape(label),'options',multiple);
    const output=host.querySelector<HTMLInputElement>('[data-combo-result]')!;
    output.name=name;
    ui.mountCombo(host.querySelector('[data-combo]')!,{choices,initial,signal});
    output.addEventListener('input',()=>latest.current?.(output.value ? output.value.split(',') : []),{signal});
  },[id,name,label,choices,multiple,initial]);
  return <div ref={ref}/>;
}
export const PersonSelect = SearchSelect;
export function PersonCard({person}: {person:Person}) {
  const ref=useWidget((host,signal)=>{
    host.innerHTML=ui.personView(person);
    ui.mountPersonCards(host,{signal});
  },[person.name,person.personKey,person.org,person.source]);
  return <div ref={ref}/>;
}
type TreeProps={nodes:Department[];initialId?:string;includeAll?:boolean;selectBranches?:boolean;onChange?:(value:Selection)=>void};
export function OrganizationTree({nodes,initialId,includeAll=false,selectBranches=true,onChange}:TreeProps) {
  const latest=useLatest(onChange);
  const ref=useWidget(host=>{ui.mountOrganizationTree(host,nodes,{initialId,includeAll,selectBranches,onChange:v=>latest.current?.(v)});},
    [nodes,initialId,includeAll,selectBranches]);
  return <div ref={ref}/>;
}
export function OrganizationPicker({nodes,value,onChange,label='所属组织',name}: {
  nodes:Department[];value:Selection|null;onChange:(value:Selection|null)=>void;label?:string;name:string;
}) {
  const [open,setOpen]=useState(false);
  const [draft,setDraft]=useState(value);
  const id=useId();
  const trigger=useRef<HTMLButtonElement>(null);
  return <div className="field"><label htmlFor={id}>{label}</label><div className="org-picker-controls">
    <button type="button" className="org-picker-trigger" ref={trigger} id={id} aria-haspopup="dialog" aria-label={label+'，'+(value?.path ?? '选择组织')} onClick={()=>{setDraft(value);setOpen(true);}}>
      <Icon name="nav-projects"/><span>{value?.path ?? '选择组织'}</span><Icon name="action-expand"/></button>
    {value && <Button className="org-picker-clear" aria-label="清除所选组织" onClick={()=>{onChange(null);trigger.current?.focus();}}><Icon name="action-close"/></Button>}</div>
    <input type="hidden" name={name} value={value?.id ?? ''}/>
    {open && <Dialog title="选择组织" variant="form" confirm="确认选择" confirmDisabled={!draft} onSubmit={form=>{
      if (!draft) return {field:form.querySelector<HTMLElement>('.tree-choice')!,message:'请选择组织。'};
      onChange(draft);
    }} onClose={()=>setOpen(false)}><OrganizationTree nodes={nodes} initialId={value?.id} onChange={setDraft}/></Dialog>}</div>;
}

export type Column<Row>={key:string;label:string;text?:(row:Row)=>string;render?:(row:Row)=>ReactNode;sort?:(row:Row)=>string;secondary?:boolean};
export type RowAction<Row>={label:string;run:(row:Row,trigger:HTMLButtonElement)=>void;visible?:(row:Row)=>boolean};
export function DataList<Row extends {id:string}>({label,itemLabel,rows,columns,rowLabel,search,filters,actions,onSelection}: {
  label:string;itemLabel:string;rows:Row[];columns:Column<Row>[];rowLabel:(row:Row)=>string;search:(row:Row)=>string;
  filters?:{key:string;label:string;options:Record<string,string>;matches?:(row:Row,value:string)=>boolean}[];
  actions?:RowAction<Row>[];onSelection?:(ids:string[])=>void;
}) {
  const id=useId().replace(/[^a-zA-Z0-9_-]/g,'_');
  const states=useRef(new Map());
  const latest=useLatest({rows,actions,rowLabel,search,onSelection});
  const lastSelection=useRef<string[]>([]);
  const [slots,setSlots]=useState<{host:Element;row:Row;column:number}[]>([]);
  const ref=useWidget((host,signal)=>{
    const cells: {key:string;label:string;render:(row:Row)=>string;sort?:(row:Row)=>string;secondary?:boolean}[]=columns.map((col,i)=>({
      key:ui.escape(col.key),label:ui.escape(col.label),sort:col.sort,secondary:col.secondary,
      render:row=>col.render ? '<span data-react-column="'+i+'"></span>' : ui.escape(col.text?.(row) ?? '')
    }));
    if(actions?.length) cells.push({key:'actions',label:'操作',render:row=>'<div class="dev-actions">'+
      (latest.current.actions ?? []).map((action,i)=>action.visible && !action.visible(row)?'':`<button type="button" class="row-action" data-row-action="${i}" aria-label="${ui.escape(action.label+' '+latest.current.rowLabel(row))}">${ui.escape(action.label)}</button>`).join('')+'</div>'});
    ui.mountList(host,{signal,key:id,label:ui.escape(label),itemLabel:ui.escape(itemLabel),rows,states:states.current,columns:cells,rowLabel,search,
      onSelection(ids:string[]) {
        if(ids.length===lastSelection.current.length && ids.every((v,i)=>v===lastSelection.current[i])) return;
        lastSelection.current=ids;
        latest.current.onSelection?.(ids);
      },
      onPaint(body:HTMLTableSectionElement,visible:Row[]) {
        if(!columns.some(col=>col.render)) {setSlots(previous=>previous.length?[]:previous);return;}
        setSlots(Array.from(body.rows).flatMap((tr,i)=>Array.from(tr.querySelectorAll<HTMLElement>('[data-react-column]'),
          host=>({host,row:visible[i],column:Number(host.dataset.reactColumn)}))));
      },
      filters:filters?.map(f=>({...f,label:ui.escape(f.label),key:ui.escape(f.key)}))});
    host.addEventListener('click',event=>{
      const button=event.target instanceof Element ? event.target.closest<HTMLButtonElement>('[data-row-action]') : null;
      if(!button) return;
      const row=latest.current.rows.find(r=>r.id===button.closest('tr')?.dataset.row);
      if(row) latest.current.actions?.[Number(button.dataset.rowAction)]?.run(row,button);
    });
  },[id,label,itemLabel,rows,columns,filters,actions,rowLabel,search]);
  return <><div className="react-list-host" ref={ref}/>{slots.map(slot=>
    createPortal(columns[slot.column]?.render?.(slot.row),slot.host,slot.row.id+'-'+slot.column))}</>;
}
