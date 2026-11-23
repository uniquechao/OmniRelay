import {useEffect, useMemo, useRef, useState, type RefObject} from 'react';
import {client} from '../../api/session';
import {Button, Status} from '../../ui/components/Elements';
import {DataList, type Column, type PageState, type RemotePage} from '../../ui/components/Widgets';
import {ui} from '../../ui/components/runtime';
import type {Guard} from '../workbench/Account';
import {projectFollowsChanged, projectStatuses, projectStatusLabel} from './projects.js';
import {openProjectProfile} from './project-profile.js';

type ProjectType = {name:string; tone:string};
type ProjectRow = {id:string; short_code?:string; name:string; status:string; project_type?:string; updated_at?:string; followed?:boolean; tone?:string};
type ProjectResult = {items:ProjectRow[]; total:number; can_create:boolean; project_types?:ProjectType[]};
const emptyRows:ProjectRow[]=[];
const nameOf=(row:ProjectRow)=>row.name;
const phases=[{key:'status',label:'项目阶段',options:Object.fromEntries(projectStatuses.map(s=>[s,projectStatusLabel(s)]))}];
const defaults={status:'active'};
const defaultSort:PageState['sort']={key:'updated_at',direction:'desc'};
const sortable=['name','project_type','status','updated_at'];
const date=new Intl.DateTimeFormat('zh-CN',{dateStyle:'short',timeStyle:'short'});
const stageTones={active:'progress',completed:'success',blocked:'danger',paused:'warning'} as const;

export function ProjectFollow({row}:{row:ProjectRow}) {
  const [followed,setFollowed]=useState(Boolean(row.followed)), [busy,setBusy]=useState(false);
  async function toggle() {
    if (busy) return;
    setBusy(true);
    try {
      await client.bind()('/api/v1/projects/'+encodeURIComponent(row.id)+'/follow',{method:followed?'DELETE':'PUT'});
      setFollowed(!followed);
      window.dispatchEvent(new Event(projectFollowsChanged));
    } catch (error) {
      if ([403,404].includes((error as {status:number}).status)) window.dispatchEvent(new Event(projectFollowsChanged));
      ui.toast('error','关注状态更新失败，请重试。');
    } finally {setBusy(false);}
  }
  return <button type="button" className="button project-follow" aria-label={'关注项目 '+row.name} aria-pressed={followed}
    aria-disabled={busy} title={followed?'取消关注':'关注项目'} onClick={toggle}>{followed?'已关注':'关注'}</button>;
}

function initialState():Partial<PageState> {
  const params=new URLSearchParams(location.hash.split('?')[1]);
  const key=params.get('sort') || '', direction=params.get('direction');
  const page=Number(params.get('page') || 1);
  return {query:params.get('q') || '',filters:{status:params.get('status') ?? 'active'},
    page:Number.isInteger(page) && page>0 && page<=100000?page-1:0,pageSize:Number(params.get('page_size') || 10),
    sort:sortable.includes(key) && (direction==='asc' || direction==='desc')?{key,direction}:defaultSort};
}

export function ProjectListPage({guard}:{guard:RefObject<Guard>}) {
  const [initial]=useState(initialState);
  const [capabilities,setCapabilities]=useState<{create:boolean;types:ProjectType[]}>({create:false,types:[]});
  const [revision,reload]=useState(0);
  const lifetime=useRef<AbortController | null>(null);
  const heading=useRef<HTMLHeadingElement>(null);
  const memory=client.scope.projects;
  const focus=useRef(memory.list===location.hash?memory.focus:undefined);
  useEffect(()=>{
    const controller=new AbortController(); lifetime.current=controller;
    if(!focus.current) heading.current?.focus({preventScroll:true});
    return ()=>controller.abort();
  },[]);

  const columns=useMemo<Column<ProjectRow>[]>(()=>[
    {key:'name',label:'项目名称',sort:nameOf,render:row=>{
      const href='#/projects/'+encodeURIComponent(row.id)+'/overview';
      return <a href={href} ref={a=>{
        if(a && focus.current===href) {
          focus.current=undefined;
          a.focus({preventScroll:true});
          a.closest('.list-scroll')?.scrollTo(Number(memory.left)||0,Number(memory.scroll)||0);
        }
      }} onClick={e=>{
        const scroller=e.currentTarget.closest('.list-scroll');
        Object.assign(memory,{list:location.hash,focus:href,scroll:scroller?.scrollTop,left:scroller?.scrollLeft});
      }}>{row.name}</a>;
    }},
    {key:'short_code',label:'项目简称',text:row=>row.short_code || '—'},
    {key:'project_type',label:'类型',sort:row=>row.project_type || '',render:row=>row.project_type
      ? <span className="kind" data-project-type-tone={row.tone || 'neutral'}>{row.project_type}</span>:'—'},
    {key:'status',label:'阶段',sort:row=>row.status,render:row=><Status tone={stageTones[row.status as keyof typeof stageTones] || 'pending'}>{projectStatusLabel(row.status)}</Status>},
    {key:'updated_at',label:'更新时间',sort:row=>row.updated_at || '',render:row=>row.updated_at?<time className="date" dateTime={row.updated_at}>{date.format(new Date(row.updated_at))}</time>:'—'},
    {key:'follow',label:'关注',render:row=><ProjectFollow key={row.id+String(row.followed)} row={row}/>},
  ],[memory]);
  const loadPage=useMemo<RemotePage<ProjectRow>>(()=>async (state,signal)=>{
    const {page,pageSize,query,filters,sort}=state;
    const params=new URLSearchParams({page:String(page+1),page_size:String(pageSize)});
    const bookmark=new URLSearchParams();
    if(query) {params.set('q',query); bookmark.set('q',query);}
    if(filters.status) params.set('status',filters.status);
    if(filters.status!=='active') bookmark.set('status',filters.status || '');
    if(page) bookmark.set('page',String(page+1));
    if(pageSize!==10) bookmark.set('page_size',String(pageSize));
    if(sort.key!==defaultSort.key || sort.direction!==defaultSort.direction) {
      for(const values of [params,bookmark]) {values.set('sort',sort.key); values.set('direction',sort.direction);}
    }
    const hash='#/projects'+(bookmark.size?'?'+bookmark:'');
    if(location.hash.split('?')[0]==='#/projects') {
      if(location.hash!==hash) history.replaceState(history.state,'',hash);
      memory.list=hash;
    }
    const result=await client.bind()('/api/v1/projects?'+params,{signal}) as ProjectResult;
    signal.throwIfAborted();
    const types=result.project_types || [];
    setCapabilities({create:result.can_create,types});
    return {total:result.total,items:result.items.map(row=>({...row,tone:types.find(t=>t.name===row.project_type)?.tone}))};
  },[memory]);
  return <main className="page" id="page" data-layout="list" data-project-list=""><header className="head">
    <h1 id="page-heading" tabIndex={-1} ref={heading}>项目列表</h1>
    {capabilities.create && <Button className="primary" aria-haspopup="dialog" onClick={e=>openProjectProfile({
      project:null,projectTypes:capabilities.types,trigger:e.currentTarget,statuses:projectStatuses,statusLabel:projectStatusLabel,
      request:client.bind(),signal:lifetime.current!.signal,navigation:guard.current,refresh:()=>reload(n=>n+1),
    })}>新建项目</Button>}
  </header><DataList key={revision} label="项目列表" itemLabel="项目" rows={emptyRows} columns={columns}
    rowLabel={nameOf} search={nameOf} filters={phases} defaultFilters={defaults} initial={initial} defaultSort={defaultSort} loadPage={loadPage}/></main>;
}
