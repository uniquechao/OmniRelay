import {loadContentScript as script} from '../../../../web/content-script.js';
import '../../../../design/prototype/vendor/quill-snow-2.0.3.css';
import '../../../../design/prototype/omni-relay-content.css';
import {useId, useState} from 'react';
import quill from '../../../../design/prototype/vendor/quill-2.0.3.js?url';
import markdown from '../../../../design/prototype/vendor/markdown-it-15.0.3.min.js?url';
import highlight from '../../../../design/prototype/vendor/highlight-11.11.1.min.js?url';
import content from '../../../../design/prototype/omni-relay-content.js?url';
import {ui,useWidget,useLatest} from './runtime';

export type RichDocument = {ops: {insert:string;attributes?:Record<string,string|number|boolean>}[]};
declare global {
  interface Window {
    OmniRelayContent: {
      richEditor(id:string,label:string): string;
      mountRich(host:Element,options:{initial?:RichDocument;signal:AbortSignal;onChange:(value:string)=>void}):void;
      markdownView():string;
      mountMarkdown(host:Element,source:string):void;
    };
  }
}
async function load() {
  await Promise.all([script(quill),script(markdown),script(highlight)]);
  await script(content);
}
function useContent(mount:(host:HTMLElement,signal:AbortSignal)=>void, deps:React.DependencyList) {
  const [attempt,retry]=useState(0);
  return useWidget((host,signal)=>{
    host.textContent='正在加载内容…';
    void load().then(()=>{if(!signal.aborted) mount(host,signal);}).catch(()=>{
      if(signal.aborted) return;
      const button=document.createElement('button');
      button.type='button'; button.className='button'; button.textContent='内容加载失败，重试';
      button.onclick=()=>retry(i=>i+1);
      host.replaceChildren(button);
    });
  },[attempt,...deps]);
}
export function RichText({name,label,initial,onChange}: {name:string;label:string;initial?:RichDocument;onChange?:(value:RichDocument)=>void}) {
  const id=useId();
  const latest=useLatest(onChange);
  const ref=useContent((host,signal)=>{
    const api=window.OmniRelayContent;
    host.innerHTML=api.richEditor(ui.escape(id),ui.escape(label));
    host.querySelector<HTMLInputElement>('[data-editor-value]')!.name=name;
    api.mountRich(host.firstElementChild!,{signal,initial,onChange:value=>latest.current?.(JSON.parse(value))});
  },[id,name,label,initial]);
  return <div ref={ref}/>;
}
export function Markdown({source}: {source:string}) {
  const ref=useContent(host=>{
    const api=window.OmniRelayContent;
    host.innerHTML=api.markdownView();
    api.mountMarkdown(host.firstElementChild!,source);
  },[source]);
  return <div ref={ref}/>;
}
