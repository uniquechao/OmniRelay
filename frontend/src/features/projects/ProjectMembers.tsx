import {useLayoutEffect,useMemo,useRef,useState} from 'react';
import {Pencil} from 'lucide-react';
import {DataList,type Column} from '../../ui/components/Widgets';
import {ui} from '../../ui/components/runtime';
import {canRemoveMember,projectMemberDialogs} from './project-members.js';
import type {Guard} from '../workbench/Account';

type Member={id:string;account_id:string;display_name:string;project_role:string;role:string;version:number;
  responsibility?:string;last_spoke_at?:string;organization_paths?:string[][]};
type Directory={members:Member[];project_roles?:{id:string;name:string;scope:string}[];can_manage:boolean;can_delegate:boolean};
type Props={data:Directory;projectId:string;request:(url:string,options:object)=>Promise<{data:Directory}>;
  signal:AbortSignal;navigation:Guard;query:string};
const stamp=new Intl.DateTimeFormat('zh-CN',{dateStyle:'short',timeStyle:'short'});
const nameOf=(person:Member)=>person.display_name;
const orgOf=(person:Member)=>person.organization_paths?.map(p=>p.join(' / ')).join('；') || '';

function RoleCell({member,roles,editable,save}:{member:Member;roles:NonNullable<Directory['project_roles']>;editable:boolean;
  save:(member:Member,role:string)=>Promise<void>}) {
  const [editing,setEditing]=useState(false),[saving,setSaving]=useState(false);
  const select=useRef<HTMLSelectElement>(null),button=useRef<HTMLButtonElement>(null);
  const title='修改 '+member.display_name+' 的项目角色';
  useLayoutEffect(()=>{
    if(editing && select.current) {
      select.current.focus();
      try { select.current.showPicker?.(); } catch { /* Keyboard input remains available. */ }
    }
  },[editing]);
  if(editing) return <span className="project-member-role"><select className="select" aria-label={title} ref={select} defaultValue={member.project_role}
    disabled={saving} onBlur={()=>{if(!saving)setEditing(false);}} onKeyDown={e=>{
      if(e.key==='Escape' && !saving) {setEditing(false); requestAnimationFrame(()=>button.current?.focus());}
    }} onChange={async e=>{const next=e.target.value; if(next!==member.project_role) {
      setSaving(true); try {await save(member,next);} finally {setSaving(false);setEditing(false);}
    }}}>{roles.map(role=><option key={role.id} value={role.id}>{role.name}</option>)}</select></span>;
  return <span className="project-member-role">{roles.find(r=>r.id===member.project_role)?.name || '—'}
    {editable && <button ref={button} data-role-edit={member.account_id} aria-label={title} aria-haspopup="listbox" title="修改项目角色" onClick={()=>setEditing(true)}><Pencil aria-hidden="true" size={16}/></button>}</span>;
}

export function ProjectMembers(props:Props) {
  const {projectId,request,signal,navigation}=props;
  const [data,setData]=useState(props.data), root=useRef<HTMLElement>(null), nextFocus=useRef('');
  const revision=useRef(0);
  const refresh=useMemo(()=>async (selector='')=>{
    const ticket=++revision.current;
    try {
      const result=await request('/api/v1/projects/'+projectId+'/members',{signal});
      if(signal.aborted || ticket!==revision.current)return;
      nextFocus.current=selector;
      setData(result.data);
    } catch {if(!signal.aborted)ui.toast('error','成员列表读取失败，请刷新重试。');}
  },[projectId,signal,request]);
  const dialogs=useMemo(()=>projectMemberDialogs({data,projectId,request,signal,navigation,refresh}),[data,projectId,request,signal,navigation,refresh]);
  const initial=useMemo(()=>({query:props.query}),[props.query]);
  const filters=useMemo(()=>[{key:'project_role',label:'项目角色',options:Object.fromEntries((data.project_roles || []).map(r=>[r.id,r.name]))}],[data.project_roles]);
  const columns=useMemo<Column<Member>[]>(()=>{
    const roles=(data.project_roles || []).filter(r=>r.scope==='project');
    const cells:Column<Member>[]=[
      {key:'name',label:'成员',render:m=><span className="project-member-person"><span>{m.display_name}</span>
        {orgOf(m) && <small title={orgOf(m)}>{orgOf(m)}</small>}</span>},
      {key:'role',label:'项目角色',render:m=><RoleCell member={m} roles={roles} editable={data.can_manage && data.can_delegate && roles.length>1}
        save={async(member,role)=>{
          try {
            await dialogs.updateMember(member,{project_role:role});
            if(!signal.aborted)ui.toast('success','项目角色已更新。');
          } catch(error) {
            const status=(error as {status:number}).status;
            if(!signal.aborted)ui.toast('error',status===409?'无法更改角色，请核对最新角色，并保留至少一位项目负责人。':
              [403,404].includes(status)?'当前账号无权修改该角色。':'角色保存失败，请重试。');
          }
          if(!signal.aborted)await refresh('[data-role-edit="'+CSS.escape(member.account_id)+'"]');
        }}/>},
      {key:'speech',label:'最后发言',render:m=>m.last_spoke_at?<time className="date" dateTime={m.last_spoke_at}>{stamp.format(new Date(m.last_spoke_at))}</time>:'—'},
      {key:'responsibility',label:'职责',render:m=><span className="project-member-responsibility">{m.responsibility || '—'}</span>},
    ];
    if(data.can_manage) cells.push({key:'actions',label:'操作',render:m=><span className="project-member-actions">
      <button data-member-edit={m.account_id} aria-haspopup="dialog" aria-label={'编辑 '+m.display_name} onClick={e=>dialogs.open(m,e.currentTarget)}>编辑</button>
      {canRemoveMember(data,m) && <button data-member-remove={m.account_id} aria-haspopup="dialog" aria-label={'移除 '+m.display_name}
        onClick={e=>dialogs.confirmRemoval(m,e.currentTarget,()=>refresh('[data-member-add]'))}>移除</button>}</span>});
    return cells;
  },[data,dialogs,signal,refresh]);
  useLayoutEffect(()=>{
    if(!nextFocus.current)return;
    const frame=requestAnimationFrame(()=>{
      (root.current?.querySelector<HTMLElement>(nextFocus.current) || root.current?.querySelector<HTMLElement>('h2'))?.focus();
      nextFocus.current='';
    });
    return ()=>cancelAnimationFrame(frame);
  },[data]);
  const search=useMemo(()=> (m:Member)=>[m.display_name,orgOf(m),m.responsibility,data.project_roles?.find(r=>r.id===m.project_role)?.name].join(' '),[data.project_roles]);
  return <section className="project-members-page" ref={root}><header className="head"><h2 tabIndex={-1}>项目成员</h2>
    {data.can_manage && <button className="button primary" data-member-add="" aria-haspopup="dialog" onClick={e=>dialogs.open(null,e.currentTarget)}>添加成员</button>}</header>
    <DataList label="项目成员" itemLabel="成员" rows={data.members} columns={columns} rowLabel={nameOf} search={search} filters={filters} selection={false} initial={initial}/></section>;
}
