import {createRoot} from 'react-dom/client'
import {useEffect,useState} from 'react'
import {ProjectTaskWorkspace} from './ProjectTaskWorkspace'
import {AuthContext,ProjectRevision} from './project-state'
import type {ProjectDetail} from './api'
import {client} from '../../../../web/session-client.js'

function LiveBoard({project,manage}:{project:ProjectDetail;manage:boolean}) {
  const [revision,setRevision]=useState(0)
  useEffect(()=>{
    const refresh=()=>{if(document.visibilityState==='visible')setRevision(v=>v+1)}
    window.addEventListener('focus',refresh)
    const timer=setInterval(refresh,30000)
    return ()=>{clearInterval(timer);window.removeEventListener('focus',refresh)}
  },[])
  return <AuthContext.Provider value={{user:{id:client.session?.user.id ?? ''}}}><ProjectRevision.Provider value={revision}><ProjectTaskWorkspace project={project} canManage={manage}/></ProjectRevision.Provider></AuthContext.Provider>
}
export function mountBoard(host:HTMLElement,project:ProjectDetail,manage:boolean,signal:AbortSignal,navigation:{confirmNavigation?:()=>Promise<boolean>}) {
  host.replaceChildren()
  const root=createRoot(host)
  const guard=async()=>window.dispatchEvent(new CustomEvent('omni-grid:before-route-change',{cancelable:true}))
  navigation.confirmNavigation=guard
  root.render(<LiveBoard project={project} manage={manage}/>)
  signal.addEventListener('abort',()=>{root.unmount();if(navigation.confirmNavigation===guard)delete navigation.confirmNavigation},{once:true})
}
