import {useLayoutEffect,useMemo,useRef,useState,type ReactNode,type RefObject} from 'react';
import {client} from '../../api/session';
import {Button, Facts, Loading, notify} from '../../ui/components/Elements';
import type {Guard,User} from '../workbench/Account';
import {feishuBotAPI} from '../../../../web/system-api.js';
import {connectionStates,checkStates,checkErrors,time} from '../../../../web/feishu-bot.js';
import {useBotCheck} from './useBotCheck';

const requestSignal=(signal:AbortSignal)=>AbortSignal.any([signal,AbortSignal.timeout(15000)]);
function errorStatus(error:unknown){return (error as {status?:number})?.status;}
function Page({title,action,children}:{title:string;action?:ReactNode;children:ReactNode}){
  const heading=useRef<HTMLHeadingElement>(null);
  useLayoutEffect(()=>heading.current?.focus({preventScroll:true}),[]);
  return <main className="page" id="page" data-layout="content">
    <header className="head"><h1 id="page-heading" ref={heading} tabIndex={-1}>{title}</h1>{action}</header>{children}
  </main>;
}

export function ConnectionSettings({locked}:{locked:boolean}){
  const [attempt,retry]=useState(0);
  const [states,setStates]=useState<Map<string,string>>();
  const [error,setError]=useState<number>();
  useLayoutEffect(()=>{
    if(locked)return;
    const controller=new AbortController();
    setStates(undefined);setError(undefined);
    void client.bind()('/api/v1/system/connections',{signal:requestSignal(controller.signal)}).then(response=>{
      if(!controller.signal.aborted)setStates(new Map((response as {items:{id:string;state:string}[]}).items.map((item)=>[item?.id,item?.state])));
    }).catch(error=>{if(!controller.signal.aborted)setError(errorStatus(error) || 503);});
    return ()=>controller.abort();
  },[attempt,locked]);
  const service=(id:string,name:string)=><><span translate="no">{name}</span> · <span data-connection={id}>{states?.get(id)==='not_configured'?'未接入':'状态未知'}</span></>;
  return <Page title="连接设置"><section className="panel"><header className="section-head"><h2>服务连接</h2></header>
    {states?<Facts items={[{label:'业务数据源',value:service('omnigrid','OmniGrid Business API')},{label:'Agent 运行环境',value:service('deepseek_harness','DeepSeek Harness')}]}/>:
      error?<><p role="alert">{error===403?'当前账号没有查看连接设置的权限。':'暂时无法读取连接设置，请重试。'}</p>
        {error!==403 && <Button onClick={()=>retry(n=>n+1)}>重试</Button>}</>:!locked && <Loading label="正在读取连接设置…"/>}
  </section></Page>;
}

type Config={enabled:boolean;app_id:string;tenant_key:string;credentials_configured:boolean;bot_name:string;oauth_callback_url:string;
  connection?:{state:string;last_heartbeat_at?:string;last_message_at?:string}};
function chip(field:string,[tone,label]:string[]){return <span className="chip" data-bot={field} data-tone={tone}>{label}</span>;}
export function BotPage({user,guard,locked}:{user:User;guard:RefObject<Guard>;locked:boolean}){
  const api=useMemo(()=>feishuBotAPI(client.bind()),[]);
  const configure=user.permissions?.['system.configure']===true;
  const operate=user.permissions?.['system.operate']===true;
  const sync=user.permissions?.['directory.sync'] && user.adminCapabilities?.users;
  const [config,setConfig]=useState<Config>();
  const [etag,setETag]=useState<string|null>(null);
  const [draft,edit]=useState<boolean|null>(null);
  const [conflict,setConflict]=useState(false);
  const [feedback,say]=useState('');
  const [readError,setReadError]=useState('');
  const [busy,setBusy]=useState('');
  const [forbidden,deny]=useState(false);
  const lifetime=useRef<AbortController|null>(null);
  const operation=useRef('');
  const toggle=useRef<HTMLInputElement>(null);
  const dialog=useRef<HTMLDialogElement|null>(null);
  const dirty=configure && draft!==null && (conflict || draft!==config?.enabled);
  const active=()=>!locked && client.phase==='active' && !!lifetime.current && !lifetime.current.signal.aborted && !forbidden;
  function start(kind:string){
    if(!active() || operation.current)return;
    operation.current=kind;setBusy(kind);
    return lifetime.current!.signal;
  }
  function finish(){operation.current='';setBusy('');}
  function accept(response:{data:Config;etag:string|null}){
    setConfig(response.data);setETag(response.etag);setReadError('');
  }
  function rejectAccess(){
    client.scope.drafts.delete('bot-check');
    deny(true);setConfig(undefined);edit(null);setETag(null);dialog.current?.close('cancel');
    setReadError('当前账号没有访问飞书 Bot 配置的权限。');
  }
  async function refresh(){
    const signal=start('read');
    if(!signal)return;
    try{
      const response=await api.status(requestSignal(signal));
      if(signal.aborted)return;
      if(dirty && etag && response.etag && response.etag!==etag){
        setConflict(true);
        say('已保存的配置发生了变更。请核对已保存状态，再次点击「保存」确认。');
      }
      accept(response);
    }catch(error){
      if(signal.aborted)return;
      setETag(null);
      if(errorStatus(error)===403)rejectAccess();
      else setReadError('无法读取飞书 Bot 状态，请刷新重试。');
    }finally{if(!signal.aborted)finish();}
  }
  async function save(){
    if(!dirty || !etag || draft===null)return false;
    const signal=start('save');
    if(!signal)return false;
    let reread=false;
    try{
      const response=await api.save(draft,etag,requestSignal(signal));
      if(signal.aborted)return false;
      accept(response);edit(null);setConflict(false);say('');notify('success','Bot 开关已保存。');
      return true;
    }catch(error){
      if(signal.aborted)return false;
      if([412,428].includes(errorStatus(error) || 0)){
        setETag(null);setConflict(true);reread=true;
        say('配置版本需要重新核对。请核对已保存状态，再次点击「保存」确认。');
      }else say(errorStatus(error)===403?'操作权限已变更，请重新核对权限。':'无法保存 Bot 开关，请重试。');
      return false;
    }finally{
      if(!signal.aborted){finish();if(reread)void refresh();}
    }
  }
  useLayoutEffect(()=>{
    const controller=new AbortController();lifetime.current=controller;
    finish();
    if(!locked)void refresh();
    return ()=>{controller.abort();dialog.current?.close('cancel');};
  },[locked]);
  useLayoutEffect(()=>{
    guard.current={hasDraft:()=>dirty,confirmNavigation:async()=>{
      if(!active() || operation.current==='save')return false;
      if(!dirty)return true;
      return new Promise(resolve=>{
        dialog.current=window.OmniRelayUI.openDialog({title:'保存修改？',variant:'confirm',tone:'warning',
          body:'<p class="dialog-note">有未保存的修改，直接离开会丢失本次更改。</p>',cancelLabel:'继续编辑',discard:'不保存',confirm:'保存并关闭',
          restoreFocus:()=>toggle.current,onClose:async(confirmed,result)=>{
            dialog.current=null;resolve(active() && (result==='discard' || (confirmed && await save())));
          }});
      });
    }};
    const warn=(event:BeforeUnloadEvent)=>{if(dirty){event.preventDefault();event.returnValue=true;}};
    window.addEventListener('beforeunload',warn);
    return ()=>{guard.current={};window.removeEventListener('beforeunload',warn);};
  });
  const check=useBotCheck(api,operate && !locked && !forbidden,refresh,rejectAccess);
  const connection=config?.connection;
  const heartbeat=time(connection?.last_heartbeat_at),received=time(connection?.last_message_at);
  return <Page title="飞书 Bot 配置" action={sync && <a className="button" href="#/system/users">前往用户管理同步组织与用户</a>}>
    <div className="toolbar"><p id="bot-load-status" role="status">{readError || (config && configure && !etag?'暂时无法获取配置版本，请刷新状态后再保存。':
      config && !config.credentials_configured?'部署尚未注入飞书应用凭据。':'')}</p>
      {!forbidden && <Button id="bot-refresh" busy={busy==='read'} disabled={locked || !!busy} onClick={refresh}>刷新状态</Button>}</div>
    {config && <div className="stack" id="bot-content">
      <section className="panel"><header className="section-head"><h2>应用信息</h2></header><Facts items={[
        ...([['app_id','App ID'],['tenant_key','租户标识']] as const).map(([key,label])=>({label,value:<span data-bot={key} translate="no">{config[key] || '—'}</span>})),
        {label:'部署凭据',value:chip('credentials_configured',config.credentials_configured?['success','已注入']:['warning','未注入'])},
        {label:'Bot 名称',value:<span data-bot="bot_name">{config.bot_name || '—'}</span>},
        {label:'OAuth 回调地址',value:<span data-bot="oauth_callback_url" translate="no">{config.oauth_callback_url || '—'}</span>}]}/></section>
      <section className="panel"><header className="section-head"><h2>Bot 开关</h2>{chip('enabled',config.enabled?['success','已保存为启用']:['pending','已保存为关闭'])}</header>
        {configure && <form id="bot-form" className="stack" onSubmit={e=>{e.preventDefault();void save();}}>
          <label><input id="bot-enabled" ref={toggle} type="checkbox" checked={draft ?? config.enabled} disabled={locked || busy==='save'}
            onChange={e=>edit(e.target.checked===config.enabled && !conflict?null:e.target.checked)}/> 启用飞书 Bot</label>
          <p hidden={!feedback} id="bot-save-feedback" role="status">{feedback}</p>
          <div><Button id="bot-save" tone="primary" type="submit" busy={busy==='save'} disabled={locked || !!busy || !dirty || !etag}>保存</Button></div></form>}</section>
      <section className="panel"><header className="section-head"><h2>连接诊断</h2>{operate &&
        <Button id="bot-test" busy={check.busy} disabled={locked} onClick={check.run}>{check.busy?'检测中…':check.retry?'重试检测':'测试连接'}</Button>}</header>
        <ul className="attention">{([['tenant_token','凭据校验'],['bot_info','Bot 信息读取']] as const).map(([name,label])=>{
          const result=check.results.find(r=>r.name===name);
          return <li key={name}>{chip(name,checkStates.get(result?.state ?? '') ?? ['pending','待检测'])}<div><strong>{label}</strong>
            <span className="aux" data-bot={name+'_detail'}>{checkErrors.get(result?.error ?? '') || time(result?.checked_at)}</span></div></li>;
        })}
        <li>{chip('connection',connectionStates.get(connection?.state ?? '') ?? ['pending','暂无状态'])}<div><strong>长连接</strong><span className="aux" data-bot="heartbeat">{heartbeat && '最近心跳 '+heartbeat}</span></div></li>
        <li>{chip('message',received?['success','已收到消息']:['pending','尚未收到'])}<div><strong>真实收信</strong><span className="aux" data-bot="message_detail">{received || '启用后，在预发布测试群 @ Bot，或向 Bot 发送私聊消息。'}</span></div></li></ul>
        <p className="context" id="bot-test-feedback" hidden={!check.feedback} role="status">{check.feedback}</p></section></div>}
  </Page>;
}
