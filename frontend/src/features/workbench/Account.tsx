import {useEffect,useRef,useState,type RefObject} from 'react';
import {Dialog} from '../../ui/components/Widgets';
import {Facts, TextField, notify} from '../../ui/components/Elements';
import {client} from '../../api/session';
import {APIKeys} from './APIKeys';

export type User=NonNullable<typeof client.session>['user'] & {
  name?:string;display_name?:string;auth_method?:string;can_change_password?:boolean
};
const roles:Record<string,string>={system_admin:'系统管理员',system_operator:'系统运维者',system_user:'普通用户',audit_viewer:'审计查看者',project_admin:'项目管理员',project_member:'项目成员'};
export function AccountTrigger({user,mobile=false}:{user:User;mobile?:boolean}){
  const name=user.name || user.display_name || '用户';
  return <button type="button" popoverTarget="account-menu" aria-label={'用户菜单，'+name} className={'account-trigger'+(mobile?' mobile-account':'')}>
    <span className="account-avatar">{Array.from(name)[0]}</span>{!mobile && <span className="account-trigger-name">{name}</span>}</button>;
}
export type Guard={confirmNavigation?:()=>Promise<boolean>;hasDraft?:()=>boolean};
export function Account({user,logout,guard}:{user:User;logout:()=>Promise<void>;guard:RefObject<Guard>}){
  const [action,setAction]=useState('');
  const pending=useRef(false);
  const menu=useRef<HTMLDivElement>(null);
  const trigger=useRef<HTMLElement|null>(null);
  const dirty=useRef(false);
  const name=user.name || user.display_name || '用户';
  const role=user.roles?.map(r=>roles[r]).filter(Boolean).join('、') || '—';
  const source=user.auth_method==='password'?'账号密码':'飞书';
  const editable=!!user.can_change_password || source==='账号密码';
  useEffect(()=>{
    const element=menu.current!;
    function position(){
      if(!element.matches(':popover-open'))return;
      const visible=[...document.querySelectorAll<HTMLElement>('.account-trigger')].find(el=>el.getClientRects().length);
      if(!visible)return;
      trigger.current=visible;
      const rect=visible.getBoundingClientRect();
      element.style.inset=Math.max(8,rect.top-element.offsetHeight-6>=8?rect.top-element.offsetHeight-6:rect.bottom+6)+'px auto auto '+Math.max(8,Math.min(rect.left,innerWidth-element.offsetWidth-8))+'px';
    }
    function opened(){position();if(element.matches(':popover-open'))element.querySelector('button')?.focus();}
    element.addEventListener('toggle',opened);window.addEventListener('resize',position);
    return ()=>{element.removeEventListener('toggle',opened);window.removeEventListener('resize',position);};
  },[]);
  useEffect(()=>{
    if(action!=='password')return;
    const warn=(event:BeforeUnloadEvent)=>{if(dirty.current){event.preventDefault();event.returnValue=true;}};
    window.addEventListener('beforeunload',warn);
    const accountGuard:Guard={hasDraft:()=>dirty.current,confirmNavigation:async()=>{
      const dialog=document.querySelector<HTMLDialogElement>('dialog:has(input[name=currentPassword])');
      if(!dirty.current || !dialog?.open)return true;
      return new Promise(resolve=>{
        const finish=(answer:boolean)=>{dialog.removeEventListener('close',closed);dialog.removeEventListener('or-dialog-guard-cancel',cancel);resolve(answer);};
        const closed=()=>finish(true), cancel=()=>finish(false);
        dialog.addEventListener('close',closed);dialog.addEventListener('or-dialog-guard-cancel',cancel);
        dialog.dispatchEvent(new Event('cancel',{cancelable:true}));
      });
    }};
    guard.current=accountGuard;
    return ()=>{dirty.current=false;guard.current={};window.removeEventListener('beforeunload',warn);};
  },[action,guard]);
  const close=()=>setAction('');
  return <><div ref={menu} id="account-menu" className="account-menu" popover="auto" aria-label="用户菜单" onKeyDown={e=>{
    const buttons=Array.from(e.currentTarget.querySelectorAll('button'));
    const i=buttons.indexOf(document.activeElement as HTMLButtonElement);
    const count=buttons.length;
    const index={ArrowDown:(i+1)%count,ArrowUp:(i+count-1)%count,Home:0,End:count-1}[e.key];
    if(index!==undefined){e.preventDefault();buttons[index].focus();}
  }}><div className="account-identity"><span className="account-avatar">{Array.from(name)[0]}</span><div><strong>{name}</strong><span>{role} · {source}</span></div></div>
    {Object.entries({profile:'个人信息',password:'修改密码',keys:'个人 API Key',logout:'退出'}).map(([id,label])=><button type="button" key={id} data-account={id} onClick={()=>{menu.current?.hidePopover();setAction(id);}}>{label}</button>)}</div>
    {action==='keys' && <Dialog title="个人 API Key" restoreFocus={()=>trigger.current} onClose={close}><APIKeys/></Dialog>}
    {action==='profile' && <Dialog title="个人信息" restoreFocus={()=>trigger.current} onClose={close}><Facts items={[{label:'用户名',value:name},{label:'角色',value:role},{label:'身份来源',value:source}]}/></Dialog>}
    {action==='password' && <Dialog title="修改密码" variant={editable?'form':'detail'} confirm={editable?'保存':undefined} restoreFocus={()=>trigger.current}
      isDirty={()=>dirty.current} onClose={close} onSubmit={async form=>{
        const current=form.elements.namedItem('currentPassword') as HTMLInputElement;
        const next=form.elements.namedItem('newPassword') as HTMLInputElement;
        const submit=form.querySelector<HTMLButtonElement>('[type=submit]')!;
        if(pending.current)return {field:current,message:'正在保存密码。'};
        pending.current=true;submit.disabled=true;
        try {await client.changePassword(current.value,next.value);if(form.isConnected && client.phase==='active')notify('success','密码已更新。');}
        catch(error){const code=(error as {status?:number}).status;return {field:current,message:code===403?'无法验证当前账号。请重新登录后再试。':code===400 || code===422?'请核对当前密码和新密码要求。':'暂时无法修改密码，请稍后再试。'};}
        finally {current.value=next.value='';dirty.current=false;pending.current=false;submit.disabled=false;}
      }}>{editable?<div onInput={e=>{dirty.current=Array.from(e.currentTarget.querySelectorAll('input')).some(f=>!!f.value);}}>
        <TextField label="当前密码" name="currentPassword" type="password" autoComplete="current-password" required/>
        <TextField label="新密码" name="newPassword" type="password" autoComplete="new-password" minLength={15} required/></div>:<p>此账号由飞书管理，请在飞书的账号安全设置中修改密码。</p>}</Dialog>}
    {action==='logout' && <Dialog title="退出工作台" variant="confirm" confirm="退出" restoreFocus={()=>trigger.current} onClose={confirmed=>{close();if(confirmed)void logout().catch(()=>notify('error','暂时无法退出，请稍后再试。'));}}>退出后将返回登录页。</Dialog>}</>;
}
