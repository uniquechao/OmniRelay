/* Standard rich-text editing and read-only Markdown presentation. */
window.OmniRelayContent = (() => {
  const { escape } = OmniRelayUI;
  const sample = [
    '## 交付准备',
    '',
    '> 周五核对验收清单，缺项由负责人补齐。',
    '',
    '- **负责人** 王宁',
    '- **当前状态** 进行中',
    '',
    '```python',
    'def pending_items(items):',
    '    return [item for item in items',
    '            if not item["done"]]',
    '```',
    '',
    '```json',
    '{',
    '  "project": "北辰交付准备",',
    '  "ready": false',
    '}',
    '```'
  ].join('\n');
  const highlight = (text, lang) => lang && hljs.getLanguage(lang)
    ? hljs.highlight(text,{language:lang, ignoreIllegals:true}).value
    : escape(text);
  const markdown = markdownit({ html:false, langPrefix:'language-' });
  const languageNames = { python:'Python', py:'Python', json:'JSON', javascript:'JavaScript', js:'JavaScript',
    typescript:'TypeScript', ts:'TypeScript', bash:'Bash', shell:'Shell', go:'Go', sql:'SQL', yaml:'YAML', xml:'XML', html:'HTML' };
  markdown.renderer.rules.fence = (tokens,index) => {
    const { info, content } = tokens[index];
    const language = info.trim().split(/\s+/)[0].toLowerCase();
    const label = languageNames[language] || language || '纯文本';
    return '<figure class="md-code"><figcaption>' + escape(label) + '</figcaption><pre tabindex="0"><code class="hljs">' +
      highlight(content,language) + '</code></pre></figure>\n';
  };

  function toolbarButton(format,label,value = '') {
    return '<button class="ql-' + format + '" type="button"' + (value ? ' value="' + value + '"' : '') +
      ' aria-label="' + label + '" title="' + label + '"></button>';
  }

  function richEditor(id, label, example = false) {
    const buttons = (...settings) => '<span class="ql-formats">' + settings.map(args => toolbarButton(...args)).join('') + '</span>';
    return `<div class="field full-field rich-editor" data-editor${example ? ' data-editor-example' : ''}>
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

  function mountRich(root, { initial, onChange, signal } = {}) {
    const toolbar = root.querySelector('[data-editor-toolbar]');
    const body = root.querySelector('[data-editor-body]');
    const value = root.querySelector('[data-editor-value]');
    const editor = new Quill(body, {
      theme:'snow', bounds:root.querySelector('.rich-control'), placeholder:'输入正文',
      modules:{ toolbar, history:{userOnly:true} },
      formats:['header','bold','italic','underline','strike','list','blockquote','code-block','link']
    });
    editor.root.setAttribute('role','textbox');
    editor.root.setAttribute('aria-multiline','true');
    editor.root.setAttribute('aria-labelledby',body.dataset.label);
    const stylePicker = toolbar.querySelector('.ql-picker-label');
    stylePicker?.setAttribute('aria-label','段落样式');
    toolbar.querySelectorAll('.ql-picker-item').forEach(item => {
      item.setAttribute('aria-label',item.dataset.value === '2' ? '标题' : item.dataset.value === '3' ? '副标题' : '正文');
    });
    const linkInput = root.querySelector('.ql-tooltip input');
    linkInput?.setAttribute('aria-label','链接地址');
    if (root.hasAttribute('data-editor-example')) {
      editor.setContents({ ops:[
        {insert:'验收安排'}, {insert:'\n',attributes:{header:2}},
        {insert:'负责人',attributes:{bold:true}}, {insert:' 王宁，周五确认验收清单。\n'},
        {insert:'核对测试环境'}, {insert:'\n',attributes:{list:'bullet'}},
        {insert:'确认回滚依据'}, {insert:'\n',attributes:{list:'bullet'}}
      ] });
    }
    if (initial) editor.setContents(initial);
    const sync = () => { value.value = JSON.stringify(editor.getContents()); onChange?.(value.value); };
    editor.on('text-change',sync);
    sync();
    root.closest('form')?.addEventListener('reset',() => {
      if (initial) editor.setContents(initial); else editor.setText('');
      editor.history.clear();
    }, { signal });
    signal?.addEventListener('abort',() => { editor.off('text-change',sync); editor.disable(); editor.scroll.observer.disconnect(); },{once:true});
  }

  function markdownView() {
    return `<div class="markdown-view" data-markdown>
      <div class="markdown-options" role="group" aria-label="Markdown 显示模式">
        <button class="button" type="button" data-markdown-mode="rendered" aria-pressed="true">渲染</button>
        <button class="button" type="button" data-markdown-mode="source" aria-pressed="false">原文</button>
      </div>
      <div class="markdown-document" data-markdown-rendered></div>
      <pre class="markdown-source" aria-label="Markdown 原文" data-markdown-source tabindex="0" hidden><code class="hljs"></code></pre>
    </div>`;
  }

  function mountMarkdown(root, source = sample) {
    const rendered = root.querySelector('[data-markdown-rendered]');
    const original = root.querySelector('[data-markdown-source]');
    rendered.innerHTML = markdown.render(source);
    original.firstElementChild.innerHTML = highlight(source,'markdown');
    const buttons = [...root.querySelectorAll('[data-markdown-mode]')];
    buttons.forEach(button => button.addEventListener('click',() => {
      const showSource = button.dataset.markdownMode === 'source';
      rendered.hidden = showSource;
      original.hidden = !showSource;
      buttons.forEach(mode => mode.setAttribute('aria-pressed',String(mode === button)));
    }));
  }

  return { richEditor, mountRich, markdownView, mountMarkdown };
})();
