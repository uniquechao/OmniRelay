import markdownScript from '../../../../design/prototype/vendor/markdown-it-15.0.3.min.js?url';
import {loadContentScript} from '../../ui/content-script.js';
let markdown;

const batchLimit = 32 * 1024 * 1024;
const fileLimit = 20 * 1024 * 1024;
const imageTypes = new Set(['image/png','image/jpeg','image/webp','image/gif']);

export function taskRequestBody(fields, files) {
  const markdown = [fields.description,fields.acceptance].join('\n');
  const uploads = [...(files || [])].filter(([url]) => markdown.includes(']('+url+')'));
  if (!uploads.length) return fields;
  if (uploads.length>32 || uploads.reduce((n,[,file])=>n+file.size,0)>batchLimit) throw new Error('每次保存最多可添加 32 个附件，合计 32 MB。');
  const form = new FormData();
  form.append('fields',JSON.stringify(fields));
  for (const [url,file] of uploads) form.append(url.slice(url.lastIndexOf('/')+1),file,file.name || '附件');
  return form;
}

// Both task editors keep the Markdown source in the named form control.
export function bindTaskMarkdown(root, projectId, files, onInput) {
  const editor=root.querySelector('textarea'), preview=root.querySelector('[data-md-preview]');
  const toggle=root.querySelector('[data-md-toggle]'), attach=root.querySelector('[data-md-attach]');
  const picker=root.querySelector('input[type=file]'), status=root.querySelector('[data-md-status]');
  const life=new AbortController(), opts={signal:life.signal};
  let editing=false;
  const objectURLs=[];
  const release=()=>{for (const url of objectURLs.splice(0)) URL.revokeObjectURL(url);};
  function paint() {
    release();
    if (!markdown && editor.value.trim()) {
      preview.textContent=editor.value;
      loadContentScript(markdownScript).then(()=>{
        markdown ??= window.markdownit({html:false,breaks:true});
        if(!life.signal.aborted && !editing)paint();
      }).catch(()=>{if(!life.signal.aborted)status.textContent='暂时无法渲染，已显示原文。';});
      return;
    }
    preview.innerHTML=markdown?.render(editor.value) || '';
    if (!editor.value.trim()) preview.textContent=editor.name==='acceptance'?'暂无验收条件':'暂无任务说明';
    for(const image of preview.querySelectorAll('img')) {
      const file=files.get(image.getAttribute('src'));
      if(file) {
        const url=URL.createObjectURL(file);objectURLs.push(url);image.src=url;
      }
    }
    for(const link of preview.querySelectorAll('a')) {
      if(files.has(link.getAttribute('href'))) {link.removeAttribute('href');link.title='保存任务后可下载';}
      else {link.target='_blank';link.rel='noopener noreferrer';}
    }
  }
  function mode(edit,focus=false) {
    editing=edit;
    editor.hidden=!edit;preview.hidden=edit;
    toggle.textContent=edit?'预览':'编辑';
    toggle.setAttribute('aria-label',(edit?'预览':'编辑')+root.dataset.mdLabel);
    if (!edit) paint();
    if(focus) (edit?editor:toggle).focus();
  }
  toggle.addEventListener('click',()=>mode(!editing,true),opts);
  attach.addEventListener('click',()=>picker.click(),opts);
  function insert(incoming) {
    if (editor.disabled || editor.readOnly || root.closest('fieldset:disabled') || !incoming.length) return;
    if(incoming.some(file=>file.size>fileLimit)){status.textContent='单个附件最大 20 MB。';return;}
    const additions=incoming.map(file=>{
      const url='/api/v1/projects/'+encodeURIComponent(projectId)+'/task-attachments/'+crypto.randomUUID();
      const name=(file.name || '附件').replace(/[\r\n]/g,' ').replace(/[\\[\]]/g,'\\$&');
      return {url,file,source:(imageTypes.has(file.type)?'!':'')+'['+name+']('+url+')'};
    });
    const start=editing?editor.selectionStart:editor.value.length,end=editing?editor.selectionEnd:start;
    const insertion='\n'+additions.map(x=>x.source).join('\n')+'\n';
    if(editor.value.length-(end-start)+insertion.length>editor.maxLength){status.textContent='附件链接超出本字段的字数上限。';return;}
    for(const {url,file} of additions) files.set(url,file);
    editor.setRangeText(insertion,start,end,'end');
    editor.dispatchEvent(new Event('input',{bubbles:true}));
    status.textContent='';
    if(editing)editor.focus();else paint();
  }
  root.addEventListener('paste',event=>{
    if(editor.readOnly || editor.disabled)return;
    const clipboard=event.clipboardData;
    const incoming=clipboard?.files.length ? Array.from(clipboard.files) :
      Array.from(clipboard?.items || []).filter(item=>item.kind==='file').map(item=>item.getAsFile()).filter(Boolean);
    if(incoming.length){event.preventDefault();insert(incoming);}
  },opts);
  picker.addEventListener('change',()=>{insert(Array.from(picker.files));picker.value='';},opts);
  editor.addEventListener('input',()=>{status.textContent='';onInput?.(editor.value);},opts);
  mode(false);
  return ()=>{life.abort();release();};
}
