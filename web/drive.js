import { openFilePreview, readLocalDocument } from './drive-preview.js';
import { fileIcon } from './file-format.js';
import { assignmentAPI } from './chat-assignment.js';
import { fileSources, fileProjects, editFileProjects, mountFileMetadata } from './drive-metadata.js';

const categories = { all:'全部', minutes:'会议纪要', docs:'飞书文档', tasks:'任务表格（多维表格）', image:'图片', video:'视频', files:'文件' };
const coverage = { pending:'等待同步', complete:'已同步', partial:'部分同步', failed:'同步失败', expired:'同步版本已过期', revoked:'访问受限', over_limit:'超过同步大小上限' };
const fileFormats = {pdf:'PDF',word:'Word',excel:'Excel',slides:'PowerPoint',zip:'ZIP',generic:'其他文件'};
const categoryLabel = row => categories[row.format_key] || categories[row.category] || '文件';
const timeText = raw => raw ? new Date(raw).toLocaleString('zh-CN') : '—';
const decimal = new Intl.NumberFormat('zh-CN', {maximumFractionDigits:1});

function sizeText(bytes) {
  if (bytes == null) return '—';
  if (bytes < 1024) return bytes + ' B';
  const power = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), 4);
  return decimal.format(bytes / 1024 ** power) + ' ' + ['B','KB','MB','GB','TB'][power];
}

export function mountDrive(page, request, signal, {preview, editProjects, projectId, initialQuery, memory = {}} = {}) {
  const { escape, mountList } = window.OmniRelayUI;
  const returning = memory.source && memory.source.projectId === projectId && memory.source.key === history.state?.driveReturn ? memory.source : null;
  delete memory.source;
  const states = returning?.states ?? new Map();
  let focusSource = returning;
  let names = new Map();
  let category = returning?.category ?? 'all';
  let activeList;
  let files = new Map();
  const filePath = path => projectId ? path + (path.includes('?') ? '&' : '?') + 'source_project=' + encodeURIComponent(projectId) : path;
  const fileRequest = (path, options) => request(path.startsWith('/api/v1/feishu/files/') ? filePath(path) : path, options);
  const listPath = projectId ? '/api/v1/projects/' + encodeURIComponent(projectId) + '/files' : '/api/v1/feishu/files';
  page.innerHTML = (projectId ? '' : '<header class="head"><h1 id="page-heading" tabindex="-1">文件网盘</h1></header>') +
    '<div class="drive-categories" role="group" aria-label="文件分类">' + Object.entries(categories).map(([id,label]) =>
      '<button type="button" class="button" aria-controls="drive-file-list" data-drive-category="' + id + '">' + label + '</button>'
    ).join('') + '</div><p data-partial role="status" hidden>部分数据暂不可用。</p><section id="drive-file-list"></section>';
  const bar = page.querySelector('.drive-categories');
  const list = page.querySelector('#drive-file-list');

  document.addEventListener('click', event => {
    const link = event.target.closest('a[data-source-file]');
    if (!link || event.defaultPrevented || event.button || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    const key = crypto.randomUUID();
    history.replaceState({...history.state, driveReturn:key}, '');
    memory.source = {key, projectId, states, category, file:link.dataset.sourceFile, route:link.hash};
  }, {signal, capture:true});

  function previewAttrs(row) {
    const ready = row.local_source_id || row.sync_state === 'partial' || (row.version_id && row.sync_state === 'complete');
    return 'data-drive-preview="' + escape(row.id) + '"' + (ready ? '' : ' disabled title="' +
      (row.sync_state === 'complete' ? '预览暂不可用' : '当前同步状态无法预览') + '"');
  }
  function show() {
    activeList?.abort();
    files.clear();
    activeList = new AbortController();
    const lifetime = AbortSignal.any([signal, activeList.signal]);
    const root = document.createElement('div');
    list.replaceChildren(root);
    for (const button of bar.children) button.setAttribute('aria-pressed', String(button.dataset.driveCategory === category));
    const view = mountList(root, {
      key:'feishu-drive-' + category, states, label:projectId ? '项目文件' : '飞书文件', itemLabel:'文件', selection:false, signal:lifetime,
      initialQuery, defaultSort:{key:'shared_at', direction:'desc'},
      filters:[
        ...(projectId ? [] : [{key:'project_id',label:'所属项目',options:{}}]),
        {key:'sync_state',label:'同步状态',options:projectId ? {...coverage, local:'本地记录'} : coverage},
        ...(category === 'files' ? [{key:'format',label:'格式',options:fileFormats}] : [])
      ],
      async loadPage(state, pageSignal) {
        page.querySelector('[data-partial]').hidden = true;
        const params = new URLSearchParams({category:category === 'files' ? 'attachments' : category, offset:state.page * state.pageSize, limit:state.pageSize});
        if (state.filters.sync_state) params.set('sync_state',state.filters.sync_state);
        if (state.query.trim()) params.set('q', state.query.trim());
        if (state.filters.format) params.set('format',state.filters.format);
        if (state.filters.project_id) params.set('project_id', state.filters.project_id);
        if (state.sort) {
          params.set('sort',state.sort.key);
          params.set('order',state.sort.direction);
        }
        const [data, choices] = await Promise.all([
          request(listPath + '?' + params, {signal:pageSignal}),
          assignmentAPI(request).choices(pageSignal).catch(() => [])
        ]);
        pageSignal.throwIfAborted();
        page.querySelector('[data-partial]').hidden = !data.partial;
        names = new Map(choices.map(item => [item.id, item.label]));
        files = new Map(data.items.map(row => [String(row.id),row]));
        if (focusSource) requestAnimationFrame(() => {
          if (pageSignal.aborted || !focusSource) return;
          const target = [...root.querySelectorAll('[data-source-file]')].find(link =>
            link.dataset.sourceFile === focusSource.file && link.hash === focusSource.route);
          (target ?? root.querySelector('[data-search]')).focus();
          focusSource = null;
        });
        return {...data, options:{
          project_id:Object.fromEntries(Object.entries(data.projects).map(([id,label]) => [id, names.get(id) || label])),
          sync_state:projectId ? {...coverage, local:'本地记录'} : coverage, format:fileFormats
        }};
      },
      columns:[
        {key:'name',label:'名称',sort:true,render:row => '<button class="drive-file-name" type="button" ' + previewAttrs(row) + '>' +
          fileIcon(row.format_key,row.format) + '<span><strong>' + escape(row.name) + '</strong><small>' + escape(row.format) + '</small></span></button>'},
        {key:'category',label:'分类',sort:true,render:row => escape(categoryLabel(row))},
        {key:'source',label:'来源群',sort:true,render:row => fileSources(row,projectId) || '—'},
        {key:'project',label:'所属项目',sort:true,render:row => row.local_source_id ? '<span class="kind">' + escape(names.get(projectId) || projectId) + '</span>' : fileProjects(row, names)},
        {key:'size',label:'大小',sort:true,render:row => '<span class="drive-file-size" title="' +
          (row.size_source === 'original' ? '原件大小' : '大小未知') + '">' + sizeText(row.size_bytes) + '</span>'},
        {key:'shared_at',label:'分享时间',sort:true,render:row => escape(timeText(row.shared_at))},
        {key:'updated_at',label:'同步状态',sort:true,render:row => {
          const updated = row.updated_at && new Date(row.updated_at);
          return escape(row.local_source_id ? '本地记录' : coverage[row.sync_state] || '同步状态未知') + (updated ? '<br><time datetime="' +
            escape(row.updated_at) + '">' + escape(updated.toLocaleString('zh-CN')) + '</time>' : '');
        }},
        {key:'preview',label:'操作',render:row => {
          const preview = '<button type="button" class="row-action" ' + previewAttrs(row) + '>预览</button>';
          if (row.local_source_id) return preview + ' <button type="button" class="row-action" data-local-download>下载</button> ' +
            '<a class="row-action" href="#/projects/' + encodeURIComponent(projectId) + '/tasks/source/document/' + encodeURIComponent(row.local_source_id) + '">查看关联任务</a>';
          if (!row.version_id) return preview;
          const path = '/api/v1/feishu/files/' + encodeURIComponent(row.id) + '/versions/' + encodeURIComponent(row.version_id) + '/content?download=1';
          return preview + ' <a class="row-action" download href="' + escape(filePath(path)) + '">下载</a>';
        }}
      ]
    });
    root.addEventListener('click', event => {
      const button = event.target.closest('[data-drive-projects], [data-drive-preview], [data-local-download]');
      if (!button || button.disabled) return;
      const row = files.get(button.closest('tr').dataset.row);
      if (!row) return;
      if (row.local_source_id) {
        void readLocalDocument(listPath + '/documents/' + encodeURIComponent(row.local_source_id), request, button, lifetime, view.refresh);
        return;
      }
      const edit = (record, opener, updated) => editFileProjects({
        row:record, trigger:opener, request:fileRequest, signal:lifetime, refresh:view.refresh, updated,
        restoreFocus:() => document.querySelector('dialog[open] [data-drive-projects="' + CSS.escape(String(record.id)) + '"]') ||
          root.querySelector('[data-drive-projects="' + CSS.escape(String(record.id)) + '"]') ||
          root.querySelector('[data-search]')
      });
      if (button.hasAttribute('data-drive-projects')) {
        if (editProjects) editProjects(row, button, view.refresh, lifetime);
        else void edit(row, button);
      } else (preview ?? openFilePreview)(row, button, view.refresh, lifetime, {
        request:fileRequest, filePath, restoreFocus:() => button.isConnected ? button :
          root.querySelector('[data-drive-preview="' + CSS.escape(String(row.id)) + '"]') || root.querySelector('[data-search]'),
        mountMetadata:(dialog, host) => mountFileMetadata(dialog, host, row,
          record => fileProjects(record, names), edit, lifetime, categoryLabel(row), sizeText(row.size_bytes), projectId)
      });
    });
  }
  bar.addEventListener('click', event => {
    const next = event.target.closest('[data-drive-category]')?.dataset.driveCategory;
    if (next && next !== category) { category = next; show(); }
  });
  show();
}
