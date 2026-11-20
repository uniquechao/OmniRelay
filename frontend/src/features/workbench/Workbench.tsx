import {BotPage,ConnectionSettings} from '../connections/Pages';
import {AuditPage} from '../audit/AuditPage';
import {useLayoutEffect,useRef,useState} from 'react';
import {client} from '../../api/session';
import {routes,destination,permitted,remember,returnPath,type Route} from '../../routes/targets';
import {Account,AccountTrigger,type Guard,type User} from './Account';
import {BusinessPage} from './BusinessPage';
import {Overview} from './Overview';
import {Breadcrumbs,Icon,ThemeToggle,WorkbenchLayout} from '../../ui/components';
import logo from '../../../../design/prototype/omni-relay-logo-bicolor.png';
import avatar from '../../../../design/prototype/omni-relay-avatar.png';
export const isLogin=(hash:string)=>hash==='#/login' || hash.startsWith('#/login?');
export function Workbench({locked,onLogout}:{locked:boolean;onLogout:()=>Promise<void>}){
  const session=client.session!;
  const user:User=session.user;
  const access=session.access;
  const view=useRef<Guard>({});
  const account=useRef<Guard>({});
  const [path,setPath]=useState(()=>isLogin(location.hash)?returnPath():'/'+location.hash);
  const [visit,setVisit]=useState(0);
  const target=destination(path,access);
  const current=useRef(target?.path ?? '/');
  const position=useRef<number>(history.state?.workbenchIndex ?? 0);
  async function canLeave(){
    return (await account.current.confirmNavigation?.() ?? true) && (await view.current.confirmNavigation?.() ?? true);
  }
  useLayoutEffect(()=>{
    remember('');
    current.current=target?.path ?? '/';
    if(target)history.replaceState({...history.state,workbenchIndex:position.current},'',target.path.slice(1));
  },[]);
  useLayoutEffect(()=>{document.title=(target?.title ?? '工作台')+' · OmniRelay';},[target?.title]);
  useLayoutEffect(()=>{
    let intent=0;
    let restoring:string|undefined;
    let confirmation:Promise<boolean>|undefined;
    const onHash=async(event:HashChangeEvent)=>{
      if(event.newURL!==location.href)return;
      const ticket=++intent;
      const requested='/'+location.hash;
      if(requested===restoring){restoring=undefined;return;}
      restoring=undefined;
      if(client.phase!=='active')return;
      const previous='/'+new URL(event.oldURL).hash;
      const scope=client.scope;
      const stillCurrent=()=>ticket===intent && scope===client.scope && requested==='/'+location.hash;
      confirmation ??= canLeave().finally(()=>{confirmation=undefined;});
      if(!await confirmation){
        if(!stillCurrent())return;
        const index=history.state?.workbenchIndex;
        if(typeof index==='number' && index!==position.current){
          restoring=previous;
          history.go(position.current-index);
        }else history.replaceState({...history.state,workbenchIndex:position.current},'',previous);
        return;
      }
      if(!stillCurrent())return;
      await client.verify();
      if(!stillCurrent())return;
      if(client.phase!=='active'){
        history.replaceState({...history.state,workbenchIndex:position.current},'',current.current);
        return;
      }
      const next=destination(requested,client.session?.access)?.path ?? requested;
      position.current=history.state?.workbenchIndex ?? position.current+1;
      history.replaceState({...history.state,workbenchIndex:position.current},'',next);
      current.current=next;
      setPath(next);
      setVisit(value=>value+1);
    };
    window.addEventListener('hashchange',onHash);
    return ()=>{intent++;window.removeEventListener('hashchange',onHash);};
  },[]);
  const visible=routes.filter(r=>permitted(r,access));
  function link(r:Route){return <a key={r.id} href={r.path.slice(1)} data-nav={r.id} data-system={r.group==='系统管理'?r.id:undefined} aria-label={r.title}
    hidden={!permitted(r,access)} aria-current={target?.route===r?'page':undefined}>{r.group==='工作空间' && <Icon name={r.icon}/>}<span>{r.title}</span></a>;}
  const groups=['工作空间','飞书','系统管理'] as const;
  const nav=groups.map(group=>{
    const entries=routes.filter(r=>r.group===group);
    if(group==='工作空间')return entries.map(link);
    const selected=target?.route.group===group;
    return <details className="nav-group" id={group==='飞书'?'feishu-nav':'system-nav'} key={group} open={selected} hidden={!entries.some(r=>permitted(r,access))} data-active={selected}>
      <summary aria-label={group}><Icon name={entries[0].icon}/><span>{group}</span><Icon name="action-expand" className="icon disclosure"/></summary><div className="nav-children">{entries.map(link)}</div></details>;
  });
  const layout=target?.route.id==='bot-chats'?'bot-chat':target?.route.id==='projects' && target.section==='项目列表'?'project':
    ['users','roles','audit'].includes(target?.route.id ?? '')?'list':'content';
  let page;
  switch(target?.route.id){
    case 'feishu-bot':
      page=<BotPage key={visit} user={user} guard={view} locked={locked}/>;
      break;
    case 'settings':
      page=<ConnectionSettings key={visit} locked={locked}/>;
      break;
    case 'audit':
      page=<AuditPage key={visit}/>;
      break;
    default:
      if(target && target.route.id!=='workbench')page=<BusinessPage key={visit} target={target} user={user} guard={view} layout={layout}/>;
  }
  return <div hidden={locked}><WorkbenchLayout page={page} navigation={nav} layout={layout}
    brand={<a className="brand" aria-label="工作台" href={destination('',access)?.path.slice(1)}><img className="wide-logo" src={logo} alt="OmniRelay"/><img className="compact-logo" src={avatar} alt="OmniRelay"/></a>}
    mobileNavigation={<><img width="29" height="29" src={avatar} alt="OmniRelay"/><select className="select" id="mobile-nav" aria-label="工作台主导航" value={target?.route.path.slice(3) ?? ''} onChange={e=>{location.hash='#/'+e.target.value;}}>
    {!target && <option value=""/>}{groups.map(group=><optgroup key={group} label={group} hidden={!visible.some(r=>r.group===group)}>{routes.filter(r=>r.group===group).map(r=><option key={r.id} value={r.path.slice(3)} hidden={!permitted(r,access)} disabled={!permitted(r,access)}>{r.title}</option>)}</optgroup>)}</select></>}
    account={<AccountTrigger user={user}/>} tools={<><ThemeToggle/><AccountTrigger user={user} mobile/></>}
    breadcrumb={target && <Breadcrumbs items={[{label:target.section},{label:target.title}]}/>}>
    {!target?<p>当前账号没有可访问的页面。</p>:<Overview links={visible}/>}
  </WorkbenchLayout>{!locked && <Account key={target?.path} user={user} guard={account} logout={async()=>{if(await canLeave())await onLogout();}}/>}</div>;
}
