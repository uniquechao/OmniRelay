import {useId, useLayoutEffect, useMemo, useRef, type ComponentProps, type ReactNode} from 'react';
import {mountRail} from '../../../../web/rail.js';
import symbols from './symbols.svg?raw';
import avatar from '../../../../design/prototype/omni-relay-avatar.png';
import {ui, type Tone, type Wait} from './runtime';

export function Icon({name, className='icon'}: {name: string; className?: string}) {
  return <svg aria-hidden="true" className={className}><use href={'#'+name}/></svg>;
}
export function VisualResources() {
  return <><div aria-hidden="true" dangerouslySetInnerHTML={{__html:symbols}}/>
    <div className="toast-region" id="toast-region" aria-label="浮动提示"/>
    <div className="sr-only" id="toast-status" role="status"/><div className="sr-only" id="toast-urgent" role="alert"/></>;
}
export function notify(level: Tone, message: string) { ui.toast(level,message); }
export function ThemeToggle() {
  useLayoutEffect(() => { window.OmniRelayTheme.syncControls(); }, []);
  return <button type="button" className="button neutral theme-toggle" data-theme-toggle="" data-theme-icon="" aria-label="切换主题"/>;
}
export function Loading(props: Wait) {
  const {size,label,progress} = props;
  const markup = useMemo(() => window.OmniRelayLoading.markup({size,label,progress}),[size,label,progress]);
  const Host = size === 'inline' ? 'span' : 'div';
  return <Host dangerouslySetInnerHTML={{__html:markup}}/>;
}
export function Button({tone='neutral',busy=false,children,className='',disabled,...props}: ComponentProps<'button'> & {
  tone?: 'neutral' | 'primary' | 'info' | 'warning' | 'danger' | 'success'; busy?: boolean;
}) {
  return <button type="button" {...props} className={`button ${tone} ${className}${busy?' or-loading-button':''}`} disabled={disabled || busy} aria-busy={busy || undefined}>
    {busy && <Loading size="inline"/>}{children}</button>;
}
export function Status({tone,children}: {tone:'pending'|'progress'|'success'|'disabled'|'warning'|'danger'|'important';children:ReactNode}) {
  return <span className="chip" data-tone={tone==='disabled'?'pending':tone}>{children}</span>;
}
export function Panel({title,children}: {title:string;children:ReactNode}) {
  return <section className="panel"><header className="section-head"><h2>{title}</h2></header>{children}</section>;
}
export function Facts({items}: {items: {label:string;value:ReactNode}[]}) {
  return <dl className="facts">{items.map(item=><div key={item.label}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}</dl>;
}
export function Multiline({children}: {children:string}) { return <div className="multiline" tabIndex={0}>{children}</div>; }
export function FormGrid(props: ComponentProps<'form'>) { return <form {...props} className={'dev-fields '+(props.className ?? '')}/>; }
export function Field({label,id,error,children}: {label:string;id:string;error?:string;children:ReactNode}) {
  return <div className="field"><label htmlFor={id}>{label}</label>{children}
    {error && <p role="alert" id={id+'-error'} className="field-error">{error}</p>}</div>;
}
export function TextField({label,error,id,...props}: ComponentProps<'input'> & {label:string;error?:string}) {
  const auto = useId(); id ??= auto;
  return <Field {...{label,id,error}}><input {...props} id={id} aria-invalid={error?true:props['aria-invalid']} aria-describedby={error?id+'-error':props['aria-describedby']}/></Field>;
}
export function TextArea({label,error,id,...props}: ComponentProps<'textarea'> & {label:string;error?:string}) {
  const auto = useId(); id ??= auto;
  return <Field {...{label,id,error}}><textarea {...props} id={id} aria-invalid={error?true:props['aria-invalid']} aria-describedby={error?id+'-error':props['aria-describedby']}/></Field>;
}
export function DateField(props: ComponentProps<typeof TextField>) { return <TextField {...props} type="date"/>; }
const day = 86400000;
const monday = (date:number) => date - (new Date(date).getUTCDay()+6)%7 * day;
const format = (date:number, options:Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('zh-CN',{...options,timeZone:'UTC'}).format(date);
export function DateStrip({mode,value,onChange}: {mode:'day'|'week';value:string;onChange:(iso:string)=>void}) {
  const week = mode==='week';
  const time = Date.parse(value+'T00:00:00Z');
  const start = week?monday(time):time;
  const year = new Date(start+3*day).getUTCFullYear();
  const end = start+6*day;
  const options: Intl.DateTimeFormatOptions = {month:'long',day:'numeric'};
  if (new Date(start).getUTCFullYear()!==new Date(end).getUTCFullYear()) options.year='numeric';
  const move = (step:number) => onChange(new Date(start+step*(week?7:1)*day).toISOString().slice(0,10));
  return <div className="date-strip"><Button className="date-arrow date-prev" aria-label={week?'上一周':'前一天'} onClick={()=>move(-1)}><Icon name="action-expand"/></Button>
    <div className="date-summary" aria-live="polite" aria-atomic="true"><strong>{week?`${year} 年第 ${1+Math.round((start-monday(Date.UTC(year,0,4)))/(7*day))} 周`:format(start,{...options,year:'numeric'})}</strong>
    <span className="aux">{week?`${format(start,options)}（周一）– ${format(end,options)}（周日）`:format(start,{weekday:'long'})}</span></div>
    <Button className="date-arrow" aria-label={week?'下一周':'后一天'} onClick={()=>move(1)}><Icon name="action-expand"/></Button></div>;
}
export function BrandMotion({children,tagline}: {children?:ReactNode;tagline?:string}) {
  const ref=useRef<HTMLDivElement>(null);
  useLayoutEffect(()=>window.OmniRelayLoginMotion(ref.current!),[]);
  return <div ref={ref} className="login-brand"><canvas className="login-motion" aria-hidden="true"/>
    {tagline && <p className="login-tagline">{tagline}</p>}<div className="login-visual"><div className="login-orbit" aria-hidden="true">
    {Array.from({length:4},(_,i)=><span key={i}/>)}<img src={avatar} width="160" height="160" alt=""/></div>{children}</div></div>;
}
export function WorkbenchLayout({brand,navigation,mobileNavigation,breadcrumb,account,tools,page,layout,children}: {
  brand?:ReactNode;navigation:ReactNode;mobileNavigation:ReactNode;breadcrumb:ReactNode;account?:ReactNode;tools?:ReactNode;page?:ReactNode;layout?:"list"|"detail"|"content"|"project"|"bot-chat";children:ReactNode;
}) {
  const ref=useRef<HTMLDivElement>(null);
  const rail=useId();
  useLayoutEffect(()=>{
    const lifetime=new AbortController();
    mountRail(ref.current!,ref.current!.querySelectorAll('.nav details'),lifetime.signal);
    return ()=>lifetime.abort();
  },[navigation]);
  return <div className="shell" ref={ref}><aside className="rail" id={rail}>{brand}<nav className="nav">{navigation}</nav>
    <div className="rail-tools"><button type="button" className="rail-toggle" aria-controls={rail}><Icon name="action-expand" className="icon toggle-icon"/><span>收起侧栏</span></button><div className="rail-foot">{account}</div></div></aside>
    <div className="rail-resizer" role="separator" tabIndex={0} aria-orientation="vertical" aria-controls={rail} aria-label="调整侧栏宽度" aria-valuemin={52} aria-valuemax={166}/>
    <div className="workspace"><header className="top"><div className="mobile-nav">{mobileNavigation}</div><nav id="breadcrumb" className="crumb" aria-label="当前位置">{breadcrumb}</nav><div className="top-tools">{tools}</div></header>{page ?? <main id="page" className="page" data-layout={layout}>{children}</main>}<VisualResources/></div></div>;
}

export function PageHeading({title,actions}: {title:string;actions?:ReactNode}) {
  return <header className="head"><h1 tabIndex={-1}>{title}</h1>{actions}</header>;
}

export function Breadcrumbs({items}: {items:{label:string;href?:string}[]}) {
  return <>{items.map(({label,href},i)=><span key={i}>
    {i>0 && <span aria-hidden="true"> / </span>}
    {href && i<items.length-1 ? <a href={href}>{label}</a> : <span aria-current={i===items.length-1?'page':undefined}>{label}</span>}
  </span>)}</>;
}
type Tab = {id:string;label:string;content:ReactNode};
export function Tabs({label,items,value,onChange}: {label:string;items:Tab[];value:string;onChange:(id:string)=>void}) {
  const base=useId();
  const active=items.find(item=>item.id===value) ?? items[0];
  if(!active) return null;
  return <><div role="tablist" aria-label={label} className="tabs">{items.map((item,i)=>
    <button key={item.id} type="button" role="tab" id={base+'-'+item.id} aria-controls={base+'-panel-'+item.id}
      aria-selected={item===active} tabIndex={item===active?0:-1} onClick={()=>onChange(item.id)} onKeyDown={event=>{
        const target={Home:0,End:items.length-1,ArrowLeft:(i+items.length-1)%items.length,ArrowRight:(i+1)%items.length}[event.key];
        if(target===undefined) return;
        event.preventDefault();onChange(items[target].id); document.getElementById(base+'-'+items[target].id)?.focus();
      }}>{item.label}</button>)}</div>{items.map(item=><div key={item.id} role="tabpanel" id={base+'-panel-'+item.id}
      aria-labelledby={base+'-'+item.id} hidden={item!==active} tabIndex={0}>{item===active && item.content}</div>)}</>;
}
