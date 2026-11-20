import { fileIcon } from './file-format.js';

const categories = { all:'全部', minutes:'会议纪要', docs:'飞书文档', tasks:'任务表格（多维表格）', files:'文件' };
const coverage = { pending:'等待同步', complete:'已同步', partial:'部分同步', failed:'同步失败', expired:'同步版本已过期', revoked:'访问受限' };
const decimal = new Intl.NumberFormat('zh-CN', {maximumFractionDigits:1});

function sizeText(bytes) {
  if (bytes == null) return '—';
  if (bytes < 1024) return bytes + ' B';
  const power = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), 4);
  return decimal.format(bytes / 1024 ** power) + ' ' + ['B','KB','MB','GB','TB'][power];
}

export function mountDrive(page, request, signal, {preview, editProjects} = {}) {
  const { escape, mountList } = window.OmniRelayUI;
  const states = new Map();
  let category = 'all';
  let activeList;
  let files = new Map();
  page.innerHTML = '<header class="head"><h1 id="page-heading" tabindex="-1">文件网盘</h1></header>' +
    '<div class="drive-categories" role="group" aria-label="文件分类">' + Object.entries(categories).map(([id,label]) =>
      '<button type="button" class="button" aria-controls="drive-file-list" data-drive-category="' + id + '">' + label + '</button>'
    ).join('') + '</div><section id="drive-file-list"></section>';
  const bar = page.querySelector('.drive-categories');
  const list = page.querySelector('#drive-file-list');

  function previewAttrs(row) {
    const ready = preview && row.version_id && row.sync_state === 'complete';
    return 'data-drive-preview="' + escape(row.id) + '"' + (ready ? '' : ' disabled title="' +
      (row.sync_state === 'complete' ? '预览暂不可用' : '当前同步状态无法预览') + '"');
  }
  function sources(row) {
    const seen = new Set();
    return row.sources.flatMap(source => {
      const route = '#/feishu/bot-chats/' + encodeURIComponent(source.chat_id) + '/' + encodeURIComponent(source.message_id);
      if (seen.has(route)) return [];
      seen.add(route);
      return '<a class="drive-source" href="' + escape(route) + '">' +
        escape(source.chat_name || source.chat_id) + ' · ' + escape(source.sender_name || '未知分享人') + '</a>';
    }).join('<br>');
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
      key:'feishu-drive-' + category, states, label:'飞书文件', itemLabel:'文件', selection:false, signal:lifetime,
      filters:[{key:'project_id',label:'所属项目',options:{}}],
      async loadPage(state, pageSignal) {
        const params = new URLSearchParams({category, offset:state.page * state.pageSize, limit:state.pageSize});
        if (state.query.trim()) params.set('q', state.query.trim());
        if (state.filters.project_id) params.set('project_id', state.filters.project_id);
        if (state.sort) {
          params.set('sort',state.sort.key);
          params.set('order',state.sort.direction);
        }
        const data = await request('/api/v1/feishu/files?' + params, {signal:pageSignal});
        pageSignal.throwIfAborted();
        files = new Map(data.items.map(row => [String(row.id),row]));
        return {...data, options:{project_id:data.projects}};
      },
      columns:[
        {key:'name',label:'名称',sort:true,render:row => '<button class="drive-file-name" type="button" ' + previewAttrs(row) + '>' +
          fileIcon(row.format_key,row.format) + '<span><strong>' + escape(row.name) + '</strong><small>' + escape(row.format) + '</small></span></button>'},
        {key:'category',label:'分类',sort:true,render:row => escape(categories[row.category] || '文件')},
        {key:'source',label:'来源',sort:true,render:sources},
        {key:'project',label:'所属项目',sort:true,render:row => '<button class="row-action drive-projects" type="button" data-drive-projects="' +
          escape(row.id) + '"' + (editProjects ? '' : ' disabled title="项目改绑暂不可用"') + '>' +
          row.project_ids.map(id => '<span class="kind">' + escape(id) + '</span>').join('') + '</button>'},
        {key:'size',label:'大小',sort:true,render:row => '<span class="drive-file-size" title="' +
          (row.size_source === 'original' ? '原件大小' : '大小未知') + '">' + sizeText(row.size_bytes) + '</span>'},
        {key:'updated_at',label:'同步状态',sort:true,render:row => {
          const updated = row.updated_at && new Date(row.updated_at);
          return escape(coverage[row.sync_state] || '同步状态未知') + (updated ? '<br><time datetime="' +
            escape(row.updated_at) + '">' + escape(updated.toLocaleString('zh-CN')) + '</time>' : '');
        }},
        {key:'preview',label:'操作',render:row => '<button type="button" class="row-action" ' + previewAttrs(row) + '>预览</button>'}
      ]
    });
    root.addEventListener('click', event => {
      const button = event.target.closest('[data-drive-projects], [data-drive-preview]');
      if (!button || button.disabled) return;
      const row = files.get(button.closest('tr').dataset.row);
      if (row) (button.hasAttribute('data-drive-projects') ? editProjects : preview)(row, button, view.refresh, lifetime);
    });
  }
  bar.addEventListener('click', event => {
    const next = event.target.closest('[data-drive-category]')?.dataset.driveCategory;
    if (next && next !== category) { category = next; show(); }
  });
  show();
}
