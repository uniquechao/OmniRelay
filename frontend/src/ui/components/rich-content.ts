import {z} from '../../api/validation.ts'
import {ui} from './runtime'
import type {RichDocument} from './Content'

const escape=ui.escape
const richDocument=z.object({ops:z.array(z.object({insert:z.string(),attributes:z.record(z.string(),z.union([z.string(),z.number(),z.boolean()])).optional()}))})
export interface MarkdownRenderer {
  render(value:string):string
  renderer:{rules:{fence?:(tokens:{info:string;content:string}[],index:number)=>string}}
}
interface RichEditor {
  root:HTMLElement; setContents(document:RichDocument):void; setText(text:string):void
  getContents():unknown; on(event:string,callback:()=>void):void; off(event:string,callback:()=>void):void
  disable():void; scroll:{observer:MutationObserver}; history:{clear():void}
}
declare global {
  interface Window {
    markdownit(options:{html:boolean;breaks?:boolean;langPrefix?:string}):MarkdownRenderer
    Quill:new(host:HTMLElement,options:object)=>RichEditor
    hljs:{getLanguage(name:string):unknown;highlight(text:string,options:{language:string;ignoreIllegals:boolean}):{value:string}}
  }
}
  const highlight = (text:string, lang:string) => lang && window.hljs.getLanguage(lang)
    ? window.hljs.highlight(text,{language:lang, ignoreIllegals:true}).value
    : escape(text);
  function renderer(){
  const markdown = window.markdownit({ html:false, langPrefix:'language-' });
  const languageNames:Record<string,string> = { python:'Python', py:'Python', json:'JSON', javascript:'JavaScript', js:'JavaScript',
    typescript:'TypeScript', ts:'TypeScript', bash:'Bash', shell:'Shell', go:'Go', sql:'SQL', yaml:'YAML', xml:'XML', html:'HTML' };
  markdown.renderer.rules.fence = (tokens,index) => {
    const { info, content } = tokens[index];
    const language = info.trim().split(/\s+/)[0].toLowerCase();
    const label = languageNames[language] || language || '纯文本';
    return '<figure class="md-code"><figcaption>' + escape(label) + '</figcaption><pre tabindex="0"><code class="hljs">' +
      highlight(content,language) + '</code></pre></figure>\n';
  };

  return markdown
}

  function toolbarButton(format:string,label:string,value = '') {
    return '<button class="ql-' + format + '" type="button"' + (value ? ' value="' + value + '"' : '') +
      ' aria-label="' + label + '" title="' + label + '"></button>';
  }

  export function richEditor(id:string, label:string) {
    const buttons = (...settings:[string,string,string?][]) => '<span class="ql-formats">' + settings.map(args => toolbarButton(...args)).join('') + '</span>';
    return `<div class="field full-field rich-editor" data-editor>
      <span class="field-label" id="${id}-label">${label}</span>
      <div class="rich-control">
        <div data-editor-toolbar role="group" aria-label="富文本格式">
          <span class="ql-formats"><select class="ql-header" aria-label="段落样式">
            <option value="2">标题</option><option value="3">副标题</option><option selected>正文</option>
          </select></span>
          ${buttons(['bold','加粗'],['italic','斜体'],['underline','下划线'],['strike','删除线'])}
          ${buttons(['list','项目符号','bullet'],['list','编号列表','ordered'])}
          ${buttons(['blockquote','引用'],['code-block','代码块'],['link','链接'],['clean','清除格式'])}
        </div>
        <div id="${id}" data-editor-body data-label="${id}-label"></div>
      </div>
      <input type="hidden" name="${id}" data-editor-value>
    </div>`;
  }

  export function mountRich(root:Element, {initial,onChange,signal}:{initial?:RichDocument;onChange:(value:RichDocument)=>void;signal:AbortSignal}) {
    const toolbar = root.querySelector<HTMLElement>('[data-editor-toolbar]');
    const body = root.querySelector<HTMLElement>('[data-editor-body]');
    const value = root.querySelector<HTMLInputElement>('[data-editor-value]');
    if(!toolbar || !body || !value)throw new Error('富文本控件缺失')
    const editor = new window.Quill(body, {
      theme:'snow', bounds:root.querySelector('.rich-control'), placeholder:'输入正文',
      modules:{ toolbar, history:{userOnly:true} },
      formats:['header','bold','italic','underline','strike','list','blockquote','code-block','link']
    });
    editor.root.setAttribute('role','textbox');
    editor.root.setAttribute('aria-multiline','true');
    editor.root.setAttribute('aria-labelledby',body.dataset.label ?? '');
    const stylePicker = toolbar.querySelector('.ql-picker-label');
    stylePicker?.setAttribute('aria-label','段落样式');
    toolbar.querySelectorAll<HTMLElement>('.ql-picker-item').forEach(item => {
      item.setAttribute('aria-label',item.dataset.value === '2' ? '标题' : item.dataset.value === '3' ? '副标题' : '正文');
    });
    const linkInput = root.querySelector('.ql-tooltip input');
    linkInput?.setAttribute('aria-label','链接地址');
    if (initial) editor.setContents(initial);
    const sync = () => { const document=richDocument.parse(editor.getContents()); value.value=JSON.stringify(document); onChange(document); };
    editor.on('text-change',sync);
    sync();
    root.closest('form')?.addEventListener('reset',() => {
      if (initial) editor.setContents(initial); else editor.setText('');
      editor.history.clear();
    }, { signal });
    signal?.addEventListener('abort',() => { editor.off('text-change',sync); editor.disable(); editor.scroll.observer.disconnect(); },{once:true});
  }

  export function markdownView() {
    return `<div class="markdown-view" data-markdown>
      <div class="markdown-options" role="group" aria-label="Markdown 显示模式">
        <button class="button" type="button" data-markdown-mode="rendered" aria-pressed="true">渲染</button>
        <button class="button" type="button" data-markdown-mode="source" aria-pressed="false">原文</button>
      </div>
      <div class="markdown-document" data-markdown-rendered></div>
      <pre class="markdown-source" aria-label="Markdown 原文" data-markdown-source tabindex="0" hidden><code class="hljs"></code></pre>
    </div>`;
  }

  export function mountMarkdown(root:Element, source:string) {
    const rendered = root.querySelector<HTMLElement>('[data-markdown-rendered]');
    const original = root.querySelector<HTMLElement>('[data-markdown-source]');
    if(!rendered || !original?.firstElementChild)throw new Error('Markdown 控件缺失');
    rendered.innerHTML = renderer().render(source);
    original.firstElementChild.innerHTML = highlight(source,'markdown');
    const buttons = [...root.querySelectorAll<HTMLButtonElement>('[data-markdown-mode]')];
    buttons.forEach(button => button.addEventListener('click',() => {
      const showSource = button.dataset.markdownMode === 'source';
      rendered.hidden = showSource;
      original.hidden = !showSource;
      buttons.forEach(mode => mode.setAttribute('aria-pressed',String(mode === button)));
    }));
  }
