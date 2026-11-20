import {useLayoutEffect,useRef} from 'react';
import {Panel, Loading} from '../../ui/components/Elements';
import {client} from '../../api/session';
import {useQuery} from '@tanstack/react-query';
import type {Route} from '../../routes/targets';
import {projectStatusLabel} from '../../../../web/projects.js';

type Project={id:string;name:string;project_type:string;status:string};
export function Overview({links}:{links:Route[]}){
  const heading=useRef<HTMLHeadingElement>(null);
  useLayoutEffect(()=>heading.current?.focus({preventScroll:true}),[]);
  const result=useQuery<{items:Project[]}>({queryKey:['workbench','projects'],retry:false,
    queryFn:({signal})=>client.bind()('/api/v1/projects?page=1&page_size=10',{signal}) as Promise<{items:Project[]}>});
  return <><h1 id="page-heading" tabIndex={-1} ref={heading}>工作台总览</h1><div className="toolbar">
    {links.filter(r=>r.id==='projects' || r.group==='系统管理').map(r=><a key={r.id} className={'button '+(r.id==='projects'?'primary':'')} href={r.path.slice(1)}>{r.id==='projects'?'查看项目列表':r.title}</a>)}
    </div><Panel title="项目列表">{result.isPending?<Loading size="page" label="正在加载项目…"/>:result.isError?<p role="status">暂时无法加载，请重试。</p>:result.data.items?.length?
      <div className="table-box"><table><thead><tr>{['项目名称','类型','阶段'].map(h=><th key={h}>{h}</th>)}</tr></thead><tbody>
        {result.data.items.map(row=><tr key={row.id}><td><a href={'#/projects/'+encodeURIComponent(row.id)+'/overview'}>{row.name}</a></td><td>{row.project_type}</td><td>{projectStatusLabel(row.status)}</td></tr>)}</tbody></table></div>:<p>没有符合条件的项目。</p>}</Panel></>;
}
