import {useLayoutEffect,useRef,useState} from 'react';
import {useLatest} from '../../ui/components/runtime';
import {client} from '../../api/session';
import {ApiError} from '../../api/session-client.js';
import {checkErrors} from './feishu-bot.js';
import type {feishuBotAPI} from '../../api/system-api.js';

type Result={name:string;state:string;error?:string;checked_at?:string};
type Check={id:string;state:string;results?:Result[]};
type Attempt={key?:string;task?:Check;feedback:string;results:Result[]};
const empty=():Attempt=>({feedback:'',results:[]});

export function useBotCheck(api:ReturnType<typeof feishuBotAPI>,enabled:boolean,refresh:()=>void,deny:()=>void){
  const refreshStatus=useLatest(refresh);
  const memory=client.scope.drafts;
  const saved=():Attempt=>memory.get('bot-check') ?? empty();
  const [view,show]=useState(saved);
  const [busy,setBusy]=useState(false);
  const active=useRef(false);
  const timer=useRef<ReturnType<typeof setTimeout>>(undefined);
  const controller=useRef<AbortController|null>(null);
  function publish(value:Attempt){memory.set('bot-check',value);show(value);}
  async function run(){
    if(!enabled || active.current || client.phase!=='active' || !controller.current)return;
    const signal=controller.current.signal;
    if(signal.aborted)return;
    active.current=true;setBusy(true);
    let attempt=saved();
    if(!attempt.key)attempt={...empty(),key:crypto.randomUUID()};
    const request=()=>AbortSignal.any([signal,AbortSignal.timeout(15000)]);
    function stop(){active.current=false;setBusy(false);}
    function failed(error:unknown,reading:boolean){
      if(signal.aborted)return;
      stop();
      const status=error instanceof ApiError?error.status:0;
      if(reading && status===403){memory.delete('bot-check');deny();return;}
      if(status>=400 && status<500 && status!==408 && status!==429)attempt=empty();
      publish({...attempt,feedback:status===403?'操作权限已变更，请重新核对权限。':reading?'无法读取检测结果，请重试。':'无法启动连接检测，请重试。'});
    }
    async function poll(){
      if(signal.aborted)return;
      try{
        const task:Check=await api.check(attempt.task!.id,request());
        if(signal.aborted)return;
        if(task.state==='queued' || task.state==='running'){
          attempt={...attempt,task,feedback:task.state==='queued'?'等待开始检测…':'正在检测飞书应用…'};
          publish(attempt);timer.current=setTimeout(poll,2000);
        }else{
          const results=task.results ?? [];
          const failure=results.find(item=>checkErrors.has(item.error ?? ''));
          publish({results,feedback:task.state==='succeeded'?'连接检测完成，请查看各项结果。':checkErrors.get(failure?.error ?? '') || '连接检测失败，请重试。'});
          stop();refreshStatus.current();
        }
      }catch(error){failed(error,true);}
    }
    try{
      if(!attempt.task){
        publish({...attempt,feedback:'正在启动连接检测…'});
        const task:Check=await api.test(attempt.key,request());
        if(signal.aborted)return;
        attempt={...attempt,task,feedback:task.state==='queued'?'等待开始检测…':'正在检测飞书应用…'};
        publish(attempt);
      }
      await poll();
    }catch(error){failed(error,false);}
  }
  useLayoutEffect(()=>{
    controller.current=new AbortController();
    active.current=false;setBusy(false);
    const attempt=saved();
    if(enabled && attempt.task)void run();
    else if(enabled && attempt.key)publish({...attempt,feedback:'尚未确认检测是否启动，请重试。'});
    return ()=>{controller.current?.abort();clearTimeout(timer.current);};
  },[enabled]);
  return {...view,busy,retry:!!view.key,run};
}
