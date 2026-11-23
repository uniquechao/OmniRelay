import {useEffect,useLayoutEffect,useState} from 'react'
import {ProjectTaskInspector} from './ProjectTaskInspector'
import {ProjectTaskWorkspace} from './ProjectTaskWorkspace'
import {AuthContext,ProjectRevision} from './project-state'
import type {ProjectDetail} from './api'
import {client} from '../../api/session-client'

export function ProjectBoard({project,manage,navigation,directTask,onCloseDirect}:{directTask?:string;onCloseDirect:()=>void;project:ProjectDetail;manage:boolean;navigation:{confirmNavigation?:()=>Promise<boolean>}}) {
  const [revision,setRevision]=useState(0)
  useLayoutEffect(()=>{
    const guard=async()=>window.dispatchEvent(new CustomEvent('omni-grid:before-route-change',{cancelable:true}))
    navigation.confirmNavigation=guard
    return ()=>{if(navigation.confirmNavigation===guard)delete navigation.confirmNavigation}
  },[navigation])
  useEffect(()=>{
    const refresh=()=>{if(document.visibilityState==='visible')setRevision(v=>v+1)}
    window.addEventListener('focus',refresh)
    const timer=setInterval(refresh,30000)
    return ()=>{clearInterval(timer);window.removeEventListener('focus',refresh)}
  },[])
  return <AuthContext.Provider value={{user:{id:client.session?.user.id ?? ''}}}><ProjectRevision.Provider value={revision}>{directTask
    ? <ProjectTaskInspector key={directTask} closeAfterSave={false} project={project} canManage={manage}
        taskId={directTask==='new'?'':directTask} mode={directTask==='new'?'create':'edit'}
        onChanged={()=>setRevision(v=>v+1)} onClose={onCloseDirect}
        onCreated={task=>{location.hash='#/projects/'+encodeURIComponent(project.id)+'/tasks/'+encodeURIComponent(task.id)}}/>
    : <ProjectTaskWorkspace project={project} canManage={manage}/>}</ProjectRevision.Provider></AuthContext.Provider>
}
