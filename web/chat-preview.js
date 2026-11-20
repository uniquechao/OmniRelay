import { fileIcon } from './file-format.js';
import { previewCanvas, renderResult } from './drive-preview.js';

const labels = {
  image: '图片', docx: '飞书文档', wiki: '飞书知识库', sheets: '飞书表格', minutes: '飞书妙记', base: '多维表格', file: '飞书文件', attachment: '文件'
};
const icons = {docx:'docs', wiki:'docs', sheets:'excel', minutes:'minutes', base:'base'};
const extensions = {pdf:'pdf', docx:'word', xlsx:'excel', pptx:'slides', zip:'zip'};
const errors = {
  message_deleted:'这条消息已撤回。', resource_not_found:'找不到此消息资源。',
  resource_forbidden:'当前账号没有权限读取此资源。', chat_access_lost:'当前账号已无法访问此会话。',
  unsupported:'此格式暂不支持预览。', encrypted:'文件已加密，无法预览。', over_limit:'内容超出预览处理限制。',
  source_unavailable:'暂时无法读取来源，请重试。', conversion_busy:'预览服务繁忙，请重试。',
  invalid_page:'此页已不可用。', invalid_sheet:'此工作表已不可用。', invalid_entry:'此目录条目已不可用。',
  failed:'无法加载预览，请重试。'
};
const retryable = new Set(['source_unavailable', 'conversion_busy', 'failed']);
const storedPreview = 'omnirelay.session.chat-preview';
const esc = value => window.OmniRelayUI.escape(value);

export function messageResources(item) {
  return item.state === 'deleted' ? [] : (item.resources ?? []);
}

export function renderMessageResource(resource, index, src, highlight) {
  const label = labels[resource.kind] ?? '文件';
  const attrs = `type="button" data-action="preview" data-resource="${index}" aria-haspopup="dialog"`;
  if (resource.kind === 'image') {
    return `<button class="chat-resource chat-resource-image" ${attrs} aria-label="放大图片">
      <span class="chat-image-fallback" hidden>图片无法显示，点击查看</span>
      <img src="${esc(src)}" width="240" height="160" alt="" loading="lazy" decoding="async"></button>`;
  }
  const name = resource.name || label;
  const iconKey = icons[resource.kind] ?? extensions[name.split('.').pop().toLowerCase()];
  return `<button class="chat-bubble chat-file chat-resource" ${attrs}>${fileIcon(iconKey, label)}<span>${highlight(name)}</span></button>`;
}

export function previewReturn(route) {
  const url = new URL(location.href);
  const result = url.searchParams.get('document_preview');
  if (!result || url.hash !== route) return;
  url.searchParams.delete('document_preview');
  history.replaceState(history.state, '', url);
  try {
    const saved = JSON.parse(sessionStorage.getItem(storedPreview) ?? 'null');
    sessionStorage.removeItem(storedPreview);
    if (saved?.route === route && saved.expires > Date.now()) return {...saved, result};
  } catch { /* 浏览器关闭存储时仍可回到消息。 */ }
}

function control(name, label, disabled = false) {
  return `<button type="button" class="button" data-preview-action="${name}"${disabled ? ' disabled' : ''}>${label}</button>`;
}

export function openMessagePreview({resource, route, restoreFocus, read, signal, authorizationResult, onClose}) {
  const label = labels[resource.kind] ?? '文件';
  const dialog = window.OmniRelayUI.openDialog({
    title: resource.url ? label : resource.name || label, drawer:'wide', restoreFocus,
    body: '<section class="drive-preview-content chat-preview"><p role="status" tabindex="-1"></p>' +
      '<div data-preview-controls></div><div data-preview-body></div></section>',
    onClose() { lifetime.abort(); onClose(); }
  });
  const lifetime = new AbortController();
  const scope = AbortSignal.any([signal, lifetime.signal]);
  const status = dialog.querySelector('[role="status"]');
  const controls = dialog.querySelector('[data-preview-controls]');
  const body = dialog.querySelector('[data-preview-body]');
  const heading = dialog.querySelector('h2');
  let page = 1, sheet = 0, entry = 0, directoryPage = 1;
  let pending;

  function sourceLink() {
    if (!resource.url) return '';
    return `<a class="button" href="${esc(resource.url)}" target="_blank" rel="noopener noreferrer">在飞书中打开</a>`;
  }

  function failure(code, kind) {
    if (code === 'document_authorization_required') {
      status.textContent = authorizationResult === 'cancelled' ? '已取消飞书授权。' :
        authorizationResult === 'unavailable' ? '飞书授权未完成，请重试或在飞书中打开。' : '授权后可在此查看飞书内容。';
      authorizationResult = undefined;
      const url = '/auth/feishu/preview?' + new URLSearchParams({kind, return_to: '/' + route});
      controls.innerHTML = `<a class="button primary" data-authorize href="${esc(url)}">授权飞书</a>` + sourceLink();
      return;
    }
    status.textContent = errors[code] ?? errors.failed;
    if (retryable.has(code)) controls.insertAdjacentHTML('beforeend', control('retry', '重试'));
    if (!['message_deleted', 'chat_access_lost', 'resource_not_found'].includes(code)) {
      controls.insertAdjacentHTML('beforeend', sourceLink());
    }
  }

  async function load() {
    pending?.abort();
    pending = new AbortController();
    const active = AbortSignal.any([scope, pending.signal]);
    const params = new URLSearchParams({page});
    if (sheet) params.set('sheet', sheet);
    if (entry) params.set('entry', entry);
    if (controls.contains(document.activeElement) || body.contains(document.activeElement)) status.focus();
    body.replaceChildren();
    controls.innerHTML = entry ? control('directory', '返回目录') : '';
    status.textContent = '正在加载预览…';
    try {
      const result = await read(params, active);
      if (active.aborted || !dialog.open) return;
      if (result instanceof Blob) {
        const canvas = await previewCanvas(result, '图片预览');
        if (active.aborted || !dialog.open) return;
        status.textContent = '';
        body.replaceChildren(canvas);
        return;
      }
      if (result.stage !== 'ready') { failure(result.stage); return; }
      const meta = result.meta;
      if (result.name && result.name !== resource.url) heading.textContent = result.name;
      if (result.png) {
        const bytes = Uint8Array.from(atob(result.png), char => char.charCodeAt(0));
        const canvas = await previewCanvas(new Blob([bytes], {type:'image/png'}), 'PDF 第 ' + page + ' 页');
        if (active.aborted || !dialog.open) return;
        body.replaceChildren(canvas);
      } else {
        const kind = entry ? meta.name.split('.').pop().toLowerCase() : result.kind;
        body.innerHTML = renderResult(kind, result.data, meta);
      }
      status.textContent = meta.pages > 1 || entry ? [entry && meta.name, '第 ' + meta.page + ' / ' + meta.pages + ' 页'].filter(Boolean).join(' · ') : '';
      if (meta.sheets?.length) controls.insertAdjacentHTML('beforeend', '<label>工作表 <select data-sheet>' +
        meta.sheets.map((name, i) => `<option value="${i}"${i === meta.sheet ? ' selected' : ''}>${esc(name)}</option>`).join('') + '</select></label>');
      if (meta.pages > 1) controls.insertAdjacentHTML('beforeend', control('prev', '上一页', page === 1) + control('next', '下一页', page === meta.pages));
      controls.insertAdjacentHTML('beforeend', sourceLink());
    } catch (error) {
      if (!active.aborted && dialog.open) failure(error.data?.error ?? 'failed', error.data?.kind);
    }
  }

  dialog.addEventListener('click', event => {
    if (event.target.closest('[data-authorize]')) {
      try {
        sessionStorage.setItem(storedPreview, JSON.stringify({route, kind:resource.kind, key:resource.key, expires:Date.now() + 300000}));
      } catch { /* 无法保存时仍可授权。 */ }
      return;
    }
    const action = event.target.closest('[data-preview-action]')?.dataset.previewAction;
    if (!action) return;
    if (action === 'prev') page--;
    if (action === 'next') page++;
    if (action.startsWith('entry-')) {
      directoryPage = page;
      entry = Number(action.slice(6));
      page = 1;
      sheet = 0;
    }
    if (action === 'directory') { entry = sheet = 0; page = directoryPage; }
    void load();
  }, {signal:scope});
  controls.addEventListener('change', event => {
    if (!event.target.matches('[data-sheet]')) return;
    sheet = Number(event.target.value);
    page = 1;
    void load();
  }, {signal:scope});
  void load();
  return dialog;
}
