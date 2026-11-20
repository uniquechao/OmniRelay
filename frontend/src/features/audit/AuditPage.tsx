import {useLayoutEffect,useMemo,useRef,useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {client} from '../../api/session';
import {DataList, Drawer, type Column, type PageState, type RemotePage} from '../../ui/components/Widgets';
import {Status} from '../../ui/components/Elements';
import {eventKinds as kinds,eventResults as results,showTime} from '../../../../web/audit-display.js';
import {listAudit} from '../../../../web/system-api.js';

type Entry = {id:string;time:string;actor:string;kind:string;action:string;target:string;result:string;
  before:string;after:string;directorySyncRunId?:string};
const filters = [{key:'kind',label:'事件类型',options:kinds},{key:'result',label:'结果',options:results}];
const defaultSort:PageState['sort'] = {key:'time',direction:'desc'};
const time = (entry:Entry)=>showTime(entry.time);
const rowLabel = (row:Entry)=>row.action;
const columns:Column<Entry>[] = [
  {key:'time',label:'时间 (UTC+08:00)',sort:time,render:row=><span className="date">{time(row)}</span>},
  {key:'action',label:'事件',sort:rowLabel,render:row=><><strong>{row.action}</strong><small className="admin-cell-sub">{kinds[row.kind] ?? row.kind}<span className="audit-compact-actor"> · {row.actor}</span></small></>},
  {key:'actor',label:'操作者',secondary:true,sort:row=>row.actor,text:row=>row.actor},
  {key:'target',label:'对象',secondary:true,sort:row=>row.target,text:row=>row.target},
  {key:'result',label:'结果',sort:row=>row.result,render:row=><Status tone={row.result==='success'?'success':['failed','denied'].includes(row.result)?'danger':'pending'}>{results[row.result] ?? row.result}</Status>}
];
const emptyRows:Entry[] = [];

function Details({items}:{items:string[][]}){
  return <dl className="admin-details">{items.map(([label,value])=><div key={label}><dt>{label}</dt><dd>{value || '—'}</dd></div>)}</dl>;
}

export function AuditPage(){
  const heading=useRef<HTMLHeadingElement>(null);
  const [entry,show]=useState<Entry|null>(null);
  const trigger=useRef<HTMLButtonElement|null>(null);
  const queries=useQueryClient();
  const loadPage=useMemo<RemotePage<Entry>>(()=>{
    const api=client.bind();
    const cursors:string[]=[];
    return async ({page,pageSize,query,filters,sort},signal)=>{
      const params={limit:pageSize,q:query,kind:filters.kind || '',result:filters.result || '',
        sort:sort.key,direction:sort.direction,...(page ? {cursor:cursors[page]} : {})};
      const queryKey=['audit-events',params];
      const cancel=()=>{void queries.cancelQueries({queryKey,exact:true});};
      signal.addEventListener('abort',cancel,{once:true});
      try {
        const result=await queries.fetchQuery({queryKey,gcTime:0,retry:false,
          queryFn:({signal})=>listAudit(api,params,signal)});
        signal.throwIfAborted();
        cursors.length=page+1;
        if(result.nextCursor)cursors.push(result.nextCursor);
        return {...result,options:{kind:kinds,result:results}};
      } finally {
        signal.removeEventListener('abort',cancel);
      }
    };
  },[queries]);
  const actions=useMemo(()=>[{label:'查看详情',run:(row:Entry,button:HTMLButtonElement)=>{
    trigger.current=button;
    show(row);
  }}],[]);
  useLayoutEffect(()=>heading.current?.focus({preventScroll:true}),[]);
  return <main id="page" className="page" data-layout="list">
    <header className="head admin-heading"><h1 ref={heading} id="page-heading" tabIndex={-1}>审计日志</h1></header>
    <DataList label="审计日志" itemLabel="操作、操作者或编号" rows={emptyRows} columns={columns} rowLabel={rowLabel} search={rowLabel}
      selection={false} filters={filters} defaultSort={defaultSort} loadPage={loadPage} actions={actions} actionLabel="详情"/>
    {entry && <Drawer title="审计详情" description={entry.id} width="wide" tone="info" onClose={()=>show(null)}
      restoreFocus={()=>trigger.current?.isConnected ? trigger.current : heading.current}>
      <section className="drawer-section"><h3>事件信息</h3><Details items={[
        ['操作时间',time(entry)+' (UTC+08:00)'],['操作者',entry.actor],['事件类型',kinds[entry.kind] ?? entry.kind],['操作',entry.action],
        ['对象',entry.target],['结果',results[entry.result] ?? entry.result],...(entry.directorySyncRunId?[['同步批次',entry.directorySyncRunId]]:[])]}/></section>
      <section className="drawer-section"><h3>变更摘要</h3><div className="admin-diff"><Details items={[['变更前',entry.before],['变更后',entry.after]]}/></div></section>
    </Drawer>}
  </main>;
}
