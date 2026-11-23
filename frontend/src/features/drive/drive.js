import { openFilePreview, readLocalDocument } from './drive-preview.js';
import { fileIcon } from './file-format.js';
import { assignmentAPI } from '../chat/chat-assignment.js';
import { fileSources, fileProjects, editFileProjects, mountFileMetadata, syncStates as coverage } from './drive-metadata.js';

export const categories = { all:'全部', minutes:'会议纪要', docs:'飞书文档', tasks:'任务表格（多维表格）', images:'图片', videos:'视频', files:'文件' };
const fileFormats = {pdf:'PDF',word:'Word',excel:'Excel',slides:'PowerPoint',zip:'ZIP',generic:'其他文件'};
const categoryLabel = row => categories[row.category] || categories[row.format_key] || '文件';
const decimal = new Intl.NumberFormat('zh-CN', {maximumFractionDigits:1});

function sizeText(bytes) {
  if (bytes == null) return '—';
  if (bytes < 1024) return bytes + ' B';
  const power = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), 4);
  return decimal.format(bytes / 1024 ** power) + ' ' + ['B','KB','MB','GB','TB'][power];
}

export function mountDrive(page, request, signal, {preview, editProjects, projectId, initialQuery, onMeetingRows, memory = {}} = {}) {
  const { escape, mountList } = window.OmniRelayUI;
  const renderTime = value => `<time datetime="${escape(value)}">${escape(new Date(value).toLocaleString('zh-CN'))}</time>`;
  const returning = memory.source && memory.source.projectId === projectId && memory.source.key === history.state?.driveReturn ? memory.source : null;
  delete memory.source;
  const states = returning?.states ?? new Map();
  let focusSource = returning;
  let names = new Map();
  let category = returning?.category ?? 'all';
  let activeList;
  let refreshList;
  let syncTimer;
  let tableSnapshot;
  let files = new Map();
  let canMark = false;
  const filePath = path => projectId ? path + (path.includes('?') ? '&' : '?') + 'source_project=' + encodeURIComponent(projectId) : path;
  const fileRequest = (path, options) => request(path.startsWith('/api/v1/feishu/files/') ? filePath(path) : path, options);
  const listPath = projectId ? '/api/v1/projects/' + encodeURIComponent(projectId) + '/files' : '/api/v1/feishu/files';
  const bar = page.querySelector('.drive-categories');
  const list = page.querySelector('#drive-file-list');
  const syncLoader = page.querySelector('[data-drive-sync-loader]');
  const syncStatus = page.querySelector('[data-drive-sync]');
  let loadingLabel;
  function showSyncLoader(label) {
    if (loadingLabel === label) return;
    loadingLabel = label;
    syncLoader.innerHTML = label ? window.OmniRelayLoading.markup({size:'compact', label}) : '';
  }
  if (!projectId) showSyncLoader('正在读取同步进度…');

  async function readSync() {
    try {
      if (document.hidden) return;
      const state = await request('/api/v1/feishu/files/sync', {signal});
      if (signal.aborted) return;
      const text = [];
      let stage = '';
      if (state.groups) {
        if (state.history_pending) stage = '正在同步群历史…';
        else if (state.unindexed_messages) stage = '正在扫描群消息…';
        else if (state.pending_sources) stage = '正在同步文件…';
        text.push('已同步 ' + state.complete_sources + ' 条来源', '待同步 ' + state.pending_sources + ' 条');
        if (state.unindexed_messages) text.push('未扫描消息 ' + state.unindexed_messages + ' 条');
        if (state.attention_sources) text.push('需处理 ' + state.attention_sources + ' 条');
        if (state.history_attention) text.push(state.history_attention + ' 个群的历史同步需要处理');
        if (state.unresolved_wiki) text.push('待解析知识库消息 ' + state.unresolved_wiki + ' 条');
      } else {
        text.push('暂无可读取的飞书群文件来源。');
      }
      showSyncLoader(stage);
      const message = text.join(' · ');
      if (syncStatus.textContent !== message) syncStatus.textContent = message;
      const latest = JSON.stringify(state);
      if (tableSnapshot === undefined) tableSnapshot = latest;
      if (tableSnapshot !== latest && !document.querySelector('dialog[open]') && !list.querySelector('tbody')?.contains(document.activeElement)) {
        tableSnapshot = latest;
        await refreshList?.({keepRows:true});
      }
    } catch {
      if (!signal.aborted) {
        showSyncLoader('');
        syncStatus.textContent = '暂时无法读取同步进度，稍后自动重试。';
      }
    } finally {
      if (!signal.aborted) syncTimer = setTimeout(readSync, 2000);
    }
  }
  signal.addEventListener('abort', () => clearTimeout(syncTimer), {once:true});


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
        const params = new URLSearchParams({category, offset:state.page * state.pageSize, limit:state.pageSize});
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
        canMark = data.can_mark_minutes === true;
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
      onPaint(body,rows) {
        if (!onMeetingRows || lifetime.aborted) return;
        onMeetingRows(Array.from(body.rows).flatMap((tr,i)=>{
          const host=tr.querySelector('[data-meeting-mark]');
          return host?[{host,row:rows[i],signal:lifetime,refresh:()=>view.refresh({keepRows:true})}]:[];
        }));
      },
      columns:[
        {key:'name',label:'名称',sort:true,render:row => '<button class="drive-file-name" type="button" ' + previewAttrs(row) + '>' +
          fileIcon(row.format_key,row.format) + '<span><strong>' + escape(row.name) + '</strong><small>' + escape(row.format) + '</small></span></button>'},
        {key:'category',label:'分类',sort:true,render:row => escape(categoryLabel(row))},
        {key:'source',label:'来源群',sort:true,render:row => fileSources(row,projectId,{compact:true}) || '—'},
        {key:'project',label:'所属项目',sort:true,render:row => row.local_source_id ? '<span class="kind">' + escape(names.get(projectId) || projectId) + '</span>' : fileProjects(row, names)},
        {key:'size',label:'大小',sort:true,render:row => '<span class="drive-file-size" title="' +
          (row.size_source === 'original' ? '原件大小' : '大小未知') + '">' + sizeText(row.size_bytes) + '</span>'},
        {key:'shared_at',label:'分享时间',sort:true,render:row => row.shared_at || row.sources?.[0]?.sent_at ? renderTime(row.shared_at || row.sources[0].sent_at) : '—'},
        {key:'updated_at',label:'同步状态',sort:true,render:row => {
          const updated = row.updated_at && new Date(row.updated_at);
          return escape(row.local_source_id ? '本地记录' : coverage[row.sync_state] || '同步状态未知') + (updated ? '<br><time datetime="' +
            escape(row.updated_at) + '">' + escape(updated.toLocaleString('zh-CN')) + '</time>' : '');
        }},
        {key:'preview',label:'操作',render:row => {
          const mark = onMeetingRows && (row.marked_minutes || (canMark && row.file_ref && ['docs','files'].includes(row.category)));
          const preview = '<button type="button" class="row-action" ' + previewAttrs(row) + '>预览</button>' + (mark?' <span data-meeting-mark></span>':'');
          if (row.local_source_id) return preview + ' <button type="button" class="row-action" data-local-download>下载</button> ' +
            '<a class="row-action" href="#/projects/' + encodeURIComponent(projectId) + '/tasks/source/document/' + encodeURIComponent(row.local_source_id) + '">查看关联任务</a>';
          if (!row.version_id) return preview;
          const path = '/api/v1/feishu/files/' + encodeURIComponent(row.id) + '/versions/' + encodeURIComponent(row.version_id) + '/content?download=1';
          return preview + ' <a class="row-action" download href="' + escape(filePath(path)) + '">下载</a>';
        }}
      ]
    });
    refreshList = view.refresh;
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
  if (!projectId) void readSync();
}
