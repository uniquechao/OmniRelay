import {useRef,useSyncExternalStore} from 'react';
import '../../../../design/prototype/omni-relay-palette.js';
import tokens from '../../../../design/prototype/omni-relay-colors.css?url';
import {Panel,notify} from './Elements';
import {ui} from './runtime';
import './palette.css';

declare global {
  interface Window { OmniRelayPalette: {swatchGroups:Record<string,[string,string][]>;states:[string,string,string][]}; }
}
function subscribe(fn:()=>void) {
  document.addEventListener('omni-relay:theme',fn);
  return ()=>document.removeEventListener('omni-relay:theme',fn);
}
export function ColorSwatch({label,tokens}: {label:string;tokens:string[]}) {
  const colors=useSyncExternalStore(subscribe,()=>{
    const styles=getComputedStyle(document.documentElement);
    return tokens.map(token=>styles.getPropertyValue(token).trim()).join(' ');
  });
  const button=useRef<HTMLButtonElement>(null);
  async function copy() {
    try { await navigator.clipboard.writeText(colors); if(button.current) notify('success','已复制 '+colors); }
    catch {
      if(!button.current) return;
      const dialog=ui.openDialog({title:'复制色值',body:'<label>色值<input readonly value="'+ui.escape(colors)+'"></label>',restoreFocus:()=>button.current});
      const input=dialog.querySelector('input')!;
      input.focus();input.select();
    }
  }
  return <button ref={button} className="palette-swatch" type="button" onClick={()=>void copy()}>
    <i aria-hidden="true" style={{backgroundColor:'var('+tokens[0]+')'}}/><strong>{label}</strong><code>{colors}</code></button>;
}
const groups:Record<string,string>={neutrals:'界面基础色',interaction:'交互色','rail-colors':'侧栏色','data-colors':'数据色','avatar-colors':'人员头像色'};
export function ColorPalette() {
  const {swatchGroups,states}=window.OmniRelayPalette;
  return <div className="stack"><a className="button" download="omni-relay-colors.css" href={tokens}>下载 CSS 色彩变量</a>
    <Panel title="品牌色"><div className="palette-group">{['indigo','teal'].map((color,i)=>
      [50,100,200,300,400,500,600,700,800,900].map(step=><ColorSwatch key={color+step} label={(i?'青绿色':'亮靛蓝')+' '+step} tokens={[`--or-${color}-${step}`]}/>))}</div></Panel>
    {Object.entries(groups).map(([id,title])=><Panel key={id} title={title}><div className="palette-group">
      {swatchGroups[id].map(([label,token])=><ColorSwatch key={token} label={label} tokens={['--or-'+token]}/>)}</div></Panel>)}
    <Panel title="状态色"><div className="palette-group">{states.map(([label,symbol,key])=>
      <ColorSwatch key={key} label={symbol+' '+label} tokens={[`--or-${key}-bg`,`--or-${key}-text`]}/>)}</div></Panel></div>;
}
