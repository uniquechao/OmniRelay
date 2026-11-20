import {useLayoutEffect,useRef,useState,type FormEvent} from 'react';
import {BrandMotion, Button, TextField, ThemeToggle} from '../../ui/components/Elements';
import {ApiError,hasHTTPS} from '../../../../web/session-client.js';
import {client} from '../../api/session';
import {returnPath} from '../../routes/targets';
import logo from '../../../../design/prototype/omni-relay-logo-bicolor.png';

const warnings:Record<string,string>={
  cancelled:'已取消飞书授权。你可以重新发起登录或使用账号密码。',
  denied:'当前飞书账号无法登录。请确认账号已由管理员开通并启用。',
  unavailable:'飞书登录暂不可用。请稍后重试或使用账号密码。'
};
const httpsWarning='账号密码操作需要 HTTPS 安全连接。';
export function Login({hash}:{hash:string}) {
  const error=new URLSearchParams(hash.split('?')[1]).get('error');
  const initial=!hasHTTPS()?httpsWarning:error?warnings[error] ?? '飞书登录未完成。你可以重新发起登录或使用账号密码。':'';
  const [message,setMessage]=useState(initial);
  const [busy,setBusy]=useState('');
  const [invalid,setInvalid]=useState('');
  const feedback=useRef<HTMLParagraphElement>(null);
  const heading=useRef<HTMLHeadingElement>(null);
  useLayoutEffect(()=>{
    heading.current?.focus();
    if(initial) feedback.current?.scrollIntoView({block:'nearest'});
    const reset=()=>setBusy('');
    window.addEventListener('pageshow',reset);
    return ()=>window.removeEventListener('pageshow',reset);
  },[initial]);
  async function login(event:FormEvent<HTMLFormElement>){
    event.preventDefault();
    if(busy || !hasHTTPS()) return;
    const form=event.currentTarget;
    const name=form.elements.namedItem('username') as HTMLInputElement;
    const password=form.elements.namedItem('password') as HTMLInputElement;
    if(!name.value.trim()){setMessage('请输入用户名。');setInvalid('username');name.focus();return;}
    setBusy('password');setMessage('');
    try {await client.login(name.value.trim(),password.value);}
    catch(error){
      const status=error instanceof ApiError?error.status:0;
      if(status===401){password.value='';setInvalid('password');password.focus();}
      setMessage(status===401?'用户名或密码不正确，请重新输入。':status===429?'登录尝试过多，请稍后再试。':
        [404,405,501].includes(status)?'当前未开通账号密码登录，请使用飞书登录。':(error as {data?:{error?:string}}).data?.error==='https_required'?httpsWarning:'暂时无法登录，请稍后再试。');
    }finally {password.value='';setBusy('');}
  }
  return <main className="login-page" id="login-page" aria-labelledby="login-heading">
    <BrandMotion tagline="让工作脉络自然浮现"><ul className="login-features" aria-label="OmniRelay 产品特性">
        <li>
          <div className="login-feature-head">
            <span className="login-feature-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="m10 8.5-4 6m8-6 4 6m-10 3h8"/><circle cx="12" cy="6.5" r="2.5"/><circle cx="5.5" cy="17.5" r="2.5"/><circle cx="18.5" cy="17.5" r="2.5"/></svg>
            </span>
            <strong>上下文聚合</strong>
          </div>
          <p>串联项目进展、任务与对话，让团队共享同一条工作脉络。</p>
        </li>
        <li>
          <div className="login-feature-head">
            <span className="login-feature-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M20 11a2 2 0 0 0 1-2V6a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h1v4l4-4h2m-5-5h9m1 3 3 3-3 3m-4-3h7"/></svg>
            </span>
            <strong>飞书联动</strong>
          </div>
          <p>在飞书查进展、收简报、分派任务，让协作在对话中继续。</p>
        </li>
      </ul></BrandMotion>
    <section className="login-content"><div className="login-top"><ThemeToggle/></div><div className="login-entry">
      <div className="login-logo"><img src={logo} width="195" height="65" alt="OmniRelay"/></div>
      <h1 id="login-heading" ref={heading} tabIndex={-1}>登录工作台</h1>
      <form className="login-form" id="password-login" aria-busy={!!busy} onSubmit={login} onInput={()=>{setInvalid('');setMessage('');}}>
        <TextField label="用户名" id="login-username" name="username" autoComplete="username" autoCapitalize="none" spellCheck={false} required disabled={!hasHTTPS()} readOnly={!!busy} aria-invalid={invalid==='username' || undefined} aria-describedby={invalid==='username'?'login-feedback':undefined}/>
        <TextField label="密码" id="login-password" name="password" type="password" autoComplete="current-password" required disabled={!hasHTTPS()} readOnly={!!busy} aria-invalid={invalid==='password' || undefined} aria-describedby={invalid==='password'?'login-feedback':undefined}/>
        <Button type="submit" className="login-button" tone="primary" busy={busy==='password'} disabled={!hasHTTPS() || !!busy}>{busy==='password'?'登录中…':'登录'}</Button>
      </form><div className="login-divider"><span>或</span></div>
      <Button id="feishu-login" className="login-button" busy={busy==='feishu'} disabled={!!busy} onClick={()=>{
        const password=document.querySelector<HTMLInputElement>('#login-password');if(password) password.value='';
        setBusy('feishu');location.assign('/auth/feishu/start?'+new URLSearchParams({return_to:returnPath()}));
      }}><svg className="icon" aria-hidden="true" viewBox="0 0 24 24"><path fill="currentColor" d="M3 11.2 20.3 3a.65.65 0 0 1 .9.8l-4.6 16.5a.7.7 0 0 1-1.1.4l-4.5-3.4-2.6 2.3a.6.6 0 0 1-1-.4l.2-4.6 9.7-8.2-11.2 6-3.1-.5c-.6-.1-.6-.5 0-.7Z"/></svg>{busy==='feishu'?'正在前往飞书…':'使用飞书登录'}</Button>
      <p ref={feedback} id="login-feedback" className="login-feedback" role="status" data-tone={initial===message?'warning':'error'} hidden={!message}>{message}</p>
    </div></section></main>;
}
