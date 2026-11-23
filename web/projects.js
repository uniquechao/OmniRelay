import { mountDrive } from './drive.js';
import { chatAPI, mountChatReader } from './chat-reader.js';
import { wireProjectMembers } from './project-members.js';
import { openProjectProfile } from './project-profile.js';

const tabs = {
  overview: '项目概览',
  members: '成员',
  chats: '群聊',
  meetings: '会议纪要',
  docs: '文件',
  reports: '日报',
  tasks: '任务看板',
  bot: 'Bot',
};
const identifier = /^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$/;
const sourceKinds = ['minutes', 'document', 'sheets', 'bitable', 'file', 'message'];
const projectStatuses = ['planned', 'active', 'paused', 'blocked', 'completed', 'archived'];

const states = {
  planned: '规划中',
  active: '进行中',
  completed: '已完成',
  blocked: '已阻塞',
  paused: '已暂停',
  archived: '已归档',
  todo: '待开始',
  in_progress: '进行中',
  done: '已完成',
  cancelled: '已取消',
  submitted: '已提交',
  pending: '待处理',
  not_applicable: '不适用',
  unknown: '待确认',
  available: '可用',
  disabled: '已停用',
  not_configured: '待配置',
  not_bound: '待绑定',
  unavailable: '暂不可用',
  succeeded: '已同步',
  historical: '历史版本',
  failed: '失败',
  access_denied: '已失去来源权限',
  body_unavailable: '正文暂不可用',
};
const label = (key) => (Object.hasOwn(states, key) ? states[key] : '待确认');
const esc = (value) =>
  String(value ?? '').replace(
    /[&<>"']/g,
    (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[ch],
  );
const empty = (message) => `<p class="empty" role="status">${esc(message)}</p>`;
const link = (href, text) => `<a href="${esc(href)}">${esc(text)}</a>`;
const panel = (title, body, action = '') =>
  `<section class="panel"><header class="section-head"><h2>${esc(title)}</h2>${action}</header>${body}</section>`;
const facts = (rows) =>
  '<dl class="facts">' +
  rows.map(([name, value, render = esc]) => `<div><dt>${esc(name)}</dt><dd>${render(value ?? '暂无')}</dd></div>`).join('') +
  '</dl>';
function projectTypeBadge(type, projectTypes) {
  if (!type) return '—';
  const tone = projectTypes?.find(({ name }) => name === type)?.tone || 'neutral';
  return `<span class="kind" data-project-type-tone="${esc(tone)}">${esc(type)}</span>`;
}
function table(headers, rows, missing) {
  if (!rows.length) return empty(missing);
  return (
    '<div class="table-box"><table><thead><tr>' +
    headers.map((x) => '<th scope="col">' + esc(x) + '</th>').join('') +
    '</tr></thead><tbody>' +
    rows.map((row) => '<tr>' + row.map((x) => '<td>' + x + '</td>').join('') + '</tr>').join('') +
    '</tbody></table></div>'
  );
}
const heading = (title) => `<h1 id="page-heading" tabindex="-1">${esc(title)}</h1>`;
const withQuery = (path, params) => path + (params.size ? '?' + params : '');
const directoryStatus = (params) => params.get('status') ?? 'active';
const projectUpdatedTime = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' });
export function projectRoute(hash) {
  if (!hash || !/^#\/projects(?:[/?]|$)/.test(hash)) return;
  const [path, query = ''] = hash.split('?');
  const [, area, id, tab = 'overview', ...parts] = path.split('/');
  const params = new URLSearchParams(query);
  if (!id && path === '#/projects') return { hash: withQuery(path, params), params };
  if (!identifier.test(id || '') || !parts.every((s) => identifier.test(s))) return;
  const base = '#/' + area + '/' + id;
  if (tab === 'tasks' && parts.length === 1 && parts[0] === 'list') {
    for (const [legacy, current] of [['q', 'task_q'], ['owner_member_id', 'task_owner'], ['milestone_id', 'task_milestone']]) {
      if (params.has(legacy) && !params.has(current)) params.set(current, params.get(legacy));
      params.delete(legacy);
    }
    if (params.get('milestone_unassigned') === 'true') params.set('task_milestone', '@none');
    for (const key of ['milestone_unassigned', 'cursor', 'column_id']) params.delete(key);
    params.set('task_layout', 'list');
    return projectRoute(withQuery(base + '/tasks', params));
  }
  if (tab === 'feishu' && !parts.length) return projectRoute(withQuery(base + '/bot', params));
  if (tab === 'brief' && parts.length <= 1)
    return projectRoute(base + '/reports/brief' + (parts.length ? '/' + parts[0] : ''));
  if (!Object.hasOwn(tabs, tab)) return;
  if (
    parts.length &&
    !(
      (tab === 'reports' && parts[0] === 'brief' && parts.length <= 2) ||
      (tab === 'tasks' &&
        (parts.length === 1 ||
          (parts.length === 3 && parts[0] === 'source' && sourceKinds.includes(parts[1])))) ||
      (tab === 'chats' && parts.length <= 2)
    )
  )
    return;
  if (parts[1] === 'message' && tab === 'tasks' && !identifier.test(params.get('chat_id') || '')) return;
  const canonical = withQuery([base, tab, ...parts].join('/'), params);
  return { hash: canonical, id, base, tab, parts, params, title: tabs[tab] };
}

function pageNumber(params) {
  const page = Number(params.get('page') || 1);
  return Number.isInteger(page) && page > 0 && page <= 100000 ? page : 1;
}

function filters(route, directory, extra = '') {
  const q = route.params.get('q') || '';
  return (
    `<form class="toolbar" data-filters><input type="search" class="search" name="q" aria-label="搜索" placeholder="${directory ? '搜索项目' : '搜索记录'}" maxlength="200" value="${esc(q)}">` +
    (directory
      ? '<select class="select" name="status" aria-label="项目状态"><option value="">全部状态</option>' +
        projectStatuses
          .map(
            (s) =>
              `<option value="${s}"${s === directoryStatus(route.params) ? ' selected' : ''}>${label(s)}</option>`,
          )
          .join('') +
        '</select>'
      : '') +
    extra + '<button class="button">查询</button></form>'
  );
}

function wireFilters(host, route) {
  host.querySelector('[data-filters]')?.addEventListener('submit', (event) => {
    event.preventDefault();
    const params = new URLSearchParams();
    for (const [key, value] of new FormData(event.currentTarget))
      if (key === 'milestone_id' && value === '@none') params.set('milestone_unassigned', 'true');
      else if (value.trim() || (!route.id && key === 'status')) params.set(key, value.trim());
    location.hash = withQuery(route.hash.split('?')[0], params);
  });
}
function pager(route, total, size) {
  const current = pageNumber(route.params);
  const move = (page, title) => {
    const params = new URLSearchParams(route.params);
    params.set('page', page);
    return link(withQuery(route.hash.split('?')[0], params), title);
  };
  return `<nav class="pr-actions" aria-label="分页">${current > 1 ? move(current - 1, '上一页') : ''}<span>第 ${current} 页 · 共 ${total} 条</span>${current * size < total ? move(current + 1, '下一页') : ''}</nav>`;
}

export function briefingState(row) {
  if (row.delivery_state === 'success' && row.delivery_confirmed_at) return '已送达';
  return (
    { not_sent: '尚未发送', pending: '投递中', failed: '投递失败', withheld: '回执受限' }[
      row.delivery_state
    ] || '送达待确认'
  );
}
function briefings(base, items) {
  return table(
    ['项目简报', '生成状态', '投递状态', '确认时间'],
    items.map((row) => [
      link(base + '/reports/brief/' + row.id, row.business_date),
      row.generated_at ? '已生成' : '生成待确认',
      briefingState(row),
      esc(row.delivery_confirmed_at || '—'),
    ]),
    '暂无项目简报。',
  );
}
export const projectFollowsChanged = 'omnirelay:project-follows-changed';

function followButton(project) {
  return `<button type="button" class="button project-follow" data-follow="${esc(project.id)}" aria-label="关注项目 ${esc(project.name)}" aria-pressed="${Boolean(project.followed)}" title="${project.followed ? '取消关注' : '关注项目'}">${project.followed ? '已关注' : '关注'}</button>`;
}

function wireFollowButtons(host, request, signal) {
  for (const button of host.querySelectorAll('button[data-follow]')) {
    button.onclick = async () => {
      if (button.getAttribute('aria-disabled') === 'true') return;
      const next = button.getAttribute('aria-pressed') !== 'true';
      button.setAttribute('aria-disabled', 'true');
      try {
        await request('/api/v1/projects/' + encodeURIComponent(button.dataset.follow) + '/follow', {method: next ? 'PUT' : 'DELETE'});
        button.setAttribute('aria-pressed', String(next));
        button.textContent = next ? '已关注' : '关注';
        button.title = next ? '取消关注' : '关注项目';
        window.dispatchEvent(new Event(projectFollowsChanged));
      } catch (error) {
        if (error.status === 403 || error.status === 404) window.dispatchEvent(new Event(projectFollowsChanged));
        if (!signal.aborted) window.OmniRelayUI.toast('error', '关注状态更新失败，请重试。');
      } finally {
        button.removeAttribute('aria-disabled');
      }
    };
  }
}

function directoryRows(items, projectTypes) {
  return table(
    ['项目名称', '类型', '阶段', '更新时间', '关注'],
    items.map((row) => [
      link('#/projects/' + row.id + '/overview', row.name),
      projectTypeBadge(row.project_type, projectTypes),
      label(row.status),
      row.updated_at ? `<time class="date" datetime="${esc(row.updated_at)}">${esc(projectUpdatedTime.format(new Date(row.updated_at)))}</time>` : '—',
      followButton(row),
    ]),
    '没有符合条件的项目。',
  );
}
export async function mountProjectSummary(host, request, signal) {
  window.OmniRelayLoading.mount(host, { size: 'page', label: '正在加载项目…' });
  try {
    const data = await request('/api/v1/projects?page=1&page_size=10', { signal });
    if (!signal.aborted) {
      host.innerHTML = panel('项目列表', directoryRows(data.items, data.project_types));
      wireFollowButtons(host, request, signal);
    }
  } catch (error) {
    if (!signal.aborted) host.innerHTML = empty(failure(error));
  } finally {
    host.removeAttribute('aria-busy');
  }
}

const taskStages = { todo: '待开始', in_progress: '进行中', blocked: '受阻', done: '已完成' };

function taskDeadline(row) {
  if (!row?.due_at) return '<span class="pr-deadline">无截止日期</span>';
  const warning = row.due_alert ? (row.due_alert_label || '临期告警') : '';
  const title = [warning, row.due_at].filter(Boolean).join(' · ');
  return `<time title="${esc(title)}" class="pr-deadline" datetime="${esc(row.due_at)}"${row.due_alert ? ' data-deadline-alert' : ''}>${row.due_alert ? '<span aria-hidden="true"><svg class="icon"><use href="#status-warning"/></svg></span><span class="sr-only">' + esc(warning) + ' · </span>' : ''}${esc(row.due_label || row.due_at)}</time>`;
}

function taskRows(base, items) {
  return table(
    ['任务', '状态', '优先级', '负责人', '里程碑', '期限'],
    items.map((row) => [
      link(base + '/tasks/' + row.id, row.title),
      esc(taskStages[row.status] || label(row.status)),
      esc(row.priority_label || '未设置'),
      esc(row.owner_member_id || '未分配'),
      esc(row.milestone_id || '未关联'),
      taskDeadline(row),
    ]),
    '暂无任务。',
  );
}

function contents(route, data, people = new Map()) {
  const { base, tab, parts } = route;
  if (tab === 'overview') {
    const { workspace, milestones } = data;
    const project = workspace.project;
    return (
      panel(
        '项目概况',
        facts([
          ['负责人', workspace.owner?.display_name],
          ['项目类型', project.project_type || '', (type) => projectTypeBadge(type, workspace.project_types)],
          ['进展速览', milestones.progress?.record?.title || '待补充'],
          ['计划开始日期', project.planned_start || '—'],
          ['计划结束日期', project.planned_end || '—'],
          ['业务时区', project.business_timezone || '—'],
          ['项目背景', project.background || '—'],
        ]),
        workspace.can_manage_profile ? '<button class="button" type="button" data-project-settings aria-haspopup="dialog">项目设置</button>' : '',
      ) +
      panel(
        '关键节点',
        table(
          ['期限', '节点', '负责人', '状态'],
          milestones.items
            .filter((node) => node.kind === 'milestone')
            .map((node) => [
              esc(node.occurs_on || '—'),
              node.task_id ? link(base + '/tasks/' + node.task_id, node.title) + (node.task?.due_at ? ' · ' + taskDeadline(node.task) : '') : esc(node.title),
              esc(node.owner_name || people.get(node.owner_member_id) || node.owner_member_id || '—'),
              taskStages[node.status] || label(node.status),
            ]),
          '暂无关键节点。',
        ) + link(base + '/tasks', '查看全部任务'),
      ) +
      panel(
        '当前关注',
        table(
          ['关注事项', '负责人'],
          milestones.items
            .filter((node) => node.kind === 'concern')
            .map((node) => [esc(node.title), esc(node.owner_name || people.get(node.owner_member_id) || node.owner_member_id || '—')]),
          '当前没有记录的关注事项。',
        ),
      )
    );
  }
  if (tab === 'members')
    return panel(
      '项目成员',
      filters(route, false) +
        table(
          ['成员', '项目角色', '职责', ...(data.can_manage ? ['操作'] : [])],
          data.members
            .filter((row) =>
              [row.display_name, (data.project_roles || []).find(role => role.id === row.project_role)?.name, row.responsibility].some((value) =>
                String(value ?? '').includes(route.params.get('q') || ''),
              ),
            )
            .map((row) => [esc(row.display_name), esc((data.project_roles || []).find(role => role.id === row.project_role)?.name || '—'), `<span class="project-member-responsibility">${esc(row.responsibility || '—')}</span>`, ...(data.can_manage ? [`<button class="button" type="button" data-member-edit="${esc(row.account_id)}">编辑<span class="sr-only"> ${esc(row.display_name)}</span></button>`] : [])]),
          '暂无项目成员。',
        ),
      data.can_manage ? '<button class="button primary" type="button" data-member-add aria-haspopup="dialog">添加成员</button>' : '',
    );
  if (tab === 'tasks' && parts.length === 1 && parts[0] === 'list') {
    for (const [legacy, current] of [['q', 'task_q'], ['owner_member_id', 'task_owner'], ['milestone_id', 'task_milestone']]) {
      if (params.has(legacy) && !params.has(current)) params.set(current, params.get(legacy));
      params.delete(legacy);
    }
    if (params.get('milestone_unassigned') === 'true') params.set('task_milestone', '@none');
    for (const key of ['milestone_unassigned', 'cursor', 'column_id']) params.delete(key);
    params.set('task_layout', 'list');
    return projectRoute(withQuery(base + '/tasks', params));
  }
  if (tab === 'feishu') {
    if (data.state === 'source_denied') return empty('已失去目标群的访问权限。');
    const plan = data.plan;
    return (
      panel(
        '项目接入',
        facts([
          ['目标群', data.target_name || plan.target_chat_id || '待绑定'],
          ['接入状态', label(data.state)],
          ['每日简报', plan.plan_enabled ? [plan.local_time, plan.timezone].join(' · ') : '已暂停'],
        ]) +
          (data.can_check
            ? '<button class="button primary" type="button" data-check>接入验证</button><div data-result role="status"></div>'
            : ''),
      ) + panel('最近回执', briefings(base, data.briefings.items))
    );
  }
  if (tab === 'reports') {
    const menu =
      '<nav class="pr-segment" aria-label="日报视图">' +
      ['成员日报', '项目简报']
        .map(
          (name, index) =>
            `<a href="${base}/reports${index ? '/brief' : ''}"${Boolean(index) === Boolean(parts.length) ? ' aria-current="page"' : ''}>${name}</a>`,
        )
        .join('') +
      '</nav>';
    if (parts.length) {
      const row = parts[1] && data.items.find((item) => item.id === parts[1]);
      return (
        menu +
        panel(
          '项目简报',
          briefings(base, data.items) +
            (row
              ? facts([
                  ['可见提交数', row.visible_submissions ?? (row.source_state === 'unavailable' ? '来源暂不可核验' : '—')],
                  ['生成时间', row.generated_at],
                  ['目标群', row.target_chat_id],
                ])
              : ''),
        )
      );
    }
    return (
      menu +
      panel(
        '成员日报',
        filters(route, false) +
          table(
            ['成员', '工作日', '日报', '提交状态'],
            data.items.map((row) => [
              esc(row.person_name),
              esc(row.work_date),
              esc(row.note),
              row.submission_state === 'pending' ? '待提交' : label(row.submission_state),
            ]),
            '暂无成员日报。',
          ) +
          pager(route, data.total, 25),
      )
    );
  }
  if (tab === 'tasks' && parts.length === 3) return panel('关联任务', taskRows(base, data.items));
  const rows = data.items.map((row) => {
    const kind = row.ref?.kind || row.resource_type || row.kind,
      id = row.ref?.source_id || row.resource_token;
    return [
      esc(row.title || '未命名') + (row.file_id && row.version_id ? ' <a download href="/api/v1/feishu/files/' +
        encodeURIComponent(row.file_id) + '/versions/' + encodeURIComponent(row.version_id) + '/content?download=1&amp;source_project=' +
        encodeURIComponent(route.id) + '">下载</a>' : ''),
      identifier.test(row.chat_id || '') && identifier.test(row.message_id || '')
        ? link('#/feishu/bot-chats/' + row.chat_id + '/' + row.message_id, '查看分享消息')
        : esc(row.editor || row.created_at || '—'),
      sourceKinds.includes(kind) && identifier.test(id || '')
        ? link(base + '/tasks/source/' + kind + '/' + id, '查看关联任务')
        : '—',
    ];
  });
  return panel(
    tabs[tab],
    filters(route, false) +
      table(['标题', '来源信息', '关联任务'], rows, '暂无记录。') +
      pager(route, data.total, 25),
  );
}

function endpoint(route) {
  const { id, tab, parts, params } = route;
  const root = '/api/v1/projects';
  const query = new URLSearchParams();
  if (!id || (['meetings', 'reports'].includes(tab) && !parts.length)) {
    query.set('page', pageNumber(params));
    query.set('page_size', 25);
  }
  if (tab !== 'members' && params.get('q')) query.set('q', params.get('q'));
  if (!id) {
    const status = directoryStatus(params);
    if (status) query.set('status', status);
    return withQuery(root, query);
  }
  let path = root + '/' + id + '/' + tab;
  if (tab === 'reports' && parts.length) path += '/briefings' + (parts[1] ? '/' + parts[1] : '');
  if (tab === 'tasks' && parts.length === 3) {
    path = root + '/' + id + '/sources/' + parts[1] + '/' + parts[2] + '/tasks';
    if (parts[1] === 'message') query.set('chat_id', params.get('chat_id'));
  } else if (tab === 'tasks' && parts.length) path += '/' + parts[0];
  return withQuery(path, query);
}

function partial(data) {
  return (
    data.coverage?.complete === false ||
    ['partial', 'unknown'].includes(data.coverage?.state) ||
    data.source_statuses?.some((row) => row.access_denied || ['partial', 'failed'].includes(row.status)) ||
    data.milestones?.progress?.state === 'unavailable' ||
    data.items?.some(
      (row) =>
        row.truncated ||
        row.coverage?.complete === false ||
        ['body_unavailable', 'historical', 'failed'].includes(row.status),
    )
  );
}
function failure(error) {
  if ([403, 404].includes(error.status)) return '当前项目或来源已不可访问。';
  if (error.status === 409) return '数据或权限已变化，请重新加载。';
  return '暂时无法加载，请重试。';
}
export async function mountProjects(host, route, request, { signal, memory, scope, navigation }) {
  const { id, base, tab, parts } = route;
  const loading = (element) => window.OmniRelayLoading.mount(element, { size: 'page', label: '正在加载…' });
  const current = () => !signal.aborted && location.hash === route.hash;
  const refresh = async () => {
    await mountProjects(host, route, request, { signal, memory, scope, navigation });
    if (current()) (host.querySelector('[data-project-settings]') || host.querySelector('#page-heading'))?.focus();
  };
  const openProfile = (trigger, projectTypes, project) => openProjectProfile({
    project, projectTypes, trigger, statuses: projectStatuses, statusLabel: label, request, signal, navigation, refresh,
  });
  const savedKey = history.state?.projectListKey;
  const saved = savedKey && scope?.read('project.' + savedKey);
  const savedRoute = typeof saved?.list === 'string' && projectRoute(saved.list);
  if (savedRoute && !savedRoute.id) Object.assign(memory, saved);
  const taskViews = memory.taskViews ??= new Map();
  if (id && tab === 'tasks' && !parts.length) {
    taskViews.set(id, route.hash);
  }
  const back = memory.list || '#/projects';
  if (id && memory.list && scope) {
    const key = savedKey || crypto.randomUUID();
    const { list, focus, scroll, windowY } = memory;
    scope.store('project.' + key, JSON.stringify({list, focus, scroll, windowY}));
    history.replaceState({...history.state, projectListKey:key}, '');
  }
  let body;
  if (id) {
    host.innerHTML = `<header class="head"><div class="project-title">${heading('项目工作台')}</div><a class="button" href="${esc(back)}">返回项目列表</a></header>
      <div class="tabs" role="tablist" aria-label="项目工作台">${Object.entries(tabs)
        .map(
          ([key, name]) =>
            `<button role="tab" id="tab-${key}" aria-selected="${key === tab}" aria-controls="project-panel" tabindex="${key === tab ? 0 : -1}" data-tab="${key}">${name}</button>`,
        )
        .join('')}</div>
      <section id="project-panel" class="stack" role="tabpanel" aria-labelledby="tab-${tab}" tabindex="0"></section>`;
    const nav = host.querySelector('.tabs');
    const selected = nav.querySelector('[aria-selected="true"]');
    if (selected && nav.scrollWidth > nav.clientWidth) {
      nav.scrollLeft = Math.max(0, selected.getBoundingClientRect().right - nav.getBoundingClientRect().right);
    }
    const switchTab = (next) => {
      memory.tabFocus = true;
      location.hash = next.dataset.tab === 'tasks' && taskViews.has(id) ? taskViews.get(id) : base + '/' + next.dataset.tab;
    };
    nav.onclick = (e) => {
      const next = e.target.closest('button');
      if (next) switchTab(next);
    };
    nav.onkeydown = (e) => {
      const buttons = [...nav.children],
        i = buttons.indexOf(document.activeElement);
      const next = { ArrowRight: (i + 1) % 8, ArrowLeft: (i + 7) % 8, Home: 0, End: 7 }[e.key];
      if (next !== undefined) {
        e.preventDefault();
        switchTab(buttons[next]);
      }
    };
    body = host.querySelector('#project-panel');
  } else {
    if (memory.list !== route.hash) memory.focus = undefined;
    memory.list = route.hash;
    host.innerHTML =
      heading('项目列表') + filters(route, true) + '<section class="panel" data-list></section>';
    body = host.querySelector('[data-list]');
    wireFilters(host, route);
  }
  loading(body);
  try {
    if (!id) {
      const data = await request(endpoint(route), { signal });
      if (!current()) return;
      if (data.can_create) {
        const toolbar = host.querySelector('[data-filters]');
        toolbar.insertAdjacentHTML('beforeend', '<button class="button primary" type="button" data-project-create aria-haspopup="dialog">新建项目</button>');
        const create = toolbar.querySelector('[data-project-create]');
        create.onclick = () => openProfile(create, data.project_types);
      }
      body.innerHTML = directoryRows(data.items, data.project_types) + pager(route, data.total, 25);
      wireFollowButtons(body, request, signal);
      body.onclick = (e) => {
        const a = e.target.closest('a');
        if (a?.hash.startsWith('#/projects/')) {
          memory.focus = a.hash;
          memory.scroll = host.scrollTop;
          memory.windowY = window.scrollY;
        }
      };
      const focus = [...body.querySelectorAll('a')].find((a) => a.hash === memory.focus);
      if (focus) {
        focus.focus({ preventScroll: true });
        host.scrollTop = memory.scroll || 0;
        window.scrollTo(0, memory.windowY || 0);
      }
      return;
    }
    const overviewURL = '/api/v1/projects/' + id + '/overview';
    const overview = request(overviewURL, { signal });
    const creatingTask = tab === 'tasks' && parts[0] === 'new';
    const boardView = tab === 'tasks' && !parts.length;
    const botSettings = tab === 'bot' && route.params.get('view') === 'connection';
    const contentRoute = botSettings ? {...route, tab: 'feishu'} : route;
    const content =
      tab === 'overview'
        ? overview
        : tab === 'chats' || tab === 'docs' || (tab === 'bot' && !botSettings)
          ? Promise.resolve()
          : creatingTask ? request('/api/v1/projects/' + id + '/tasks', {signal}) : boardView ? request('/api/v1/projects/'+id+'/board?limit=1',{signal}) : request(endpoint(contentRoute), { signal });
    const members = boardView || tab === 'overview' ? request('/api/v1/projects/' + id + '/members', { signal }) : undefined;
    const [header, result, directory] = await Promise.allSettled([overview, content, members]);
    if (!current()) return;
    if (header.status === 'rejected' && [403, 404].includes(header.reason.status)) throw header.reason;
    if (result.status === 'rejected') throw result.reason;
    const workspace = header.value?.data.workspace;
    const project = workspace?.project;
    if (project) {
      const title = host.querySelector('#page-heading');
      title.textContent = project.name || project.id;
      host.querySelector('.project-title').insertAdjacentHTML('beforeend', followButton(project));
      wireFollowButtons(host, request, signal);
      if (project.project_type)
        title.insertAdjacentHTML('beforebegin', projectTypeBadge(project.project_type, workspace.project_types));
      const tone =
        { active: 'progress', completed: 'success', blocked: 'danger', paused: 'warning' }[project.status] ||
        'pending';
      title.insertAdjacentHTML(
        'afterend',
        `<span class="chip" data-tone="${esc(tone)}">${label(project.status)}</span>`,
      );
    }
    if (memory.tabFocus) {
      host.querySelector('[aria-selected=true]').focus();
      memory.tabFocus = false;
    }
    if (tab === 'bot' && !botSettings) {
      const { mountProjectBot } = await import('./project-bot.jsx');
      if (current()) mountProjectBot(body, id, base, request, signal);
      return;
    }
    if (tab === 'docs') {
      mountDrive(body, request, signal, { projectId:id, memory:memory.files ??= {}, initialQuery:route.params.get('q') || '' });
      return;
    }
    if (tab === 'chats') {
      let chatHash = route.hash;
      const reader = chatAPI(request, '/api/v1/projects/' + id + '/chat-reader');
      delete reader.refresh;
      mountChatReader(body, reader, {
        signal,
        project: true,
        projectId: id,
        draftScope: scope,
        chatId: parts[0],
        messageId: parts[1],
        onLocation(chat, message) {
          if (signal.aborted || location.hash !== chatHash) return;
          chatHash = [base, 'chats', chat, message].filter(Boolean).join('/');
          history.replaceState(history.state, '', chatHash);
        },
      });
      return;
    }
    const data = result.value.data ?? result.value;
    if (boardView) {
      const {mountBoard}=await import('../frontend/src/features/task-board/mount.tsx');
      if (!current()) return;
      if (directory.status==='rejected') throw directory.reason;
      const actions=[];
      if (data.can_comment) actions.push('comment');
      if (data.can_create) actions.push('create_task','update_own_task');
      if (data.can_configure) actions.push('configure_board');
      mountBoard(body,{id,name:project?.name || id,business_timezone:data.business_timezone,current_member_id:data.member_id,
        actions,milestones:(header.value?.data.milestones?.items || []).filter(node => node.kind === 'milestone' && node.id),members:(directory.value?.data?.members || []).map(member=>({...member,id:member.member_id || member.id}))},data.can_assign,signal,navigation);
      return;
    }
    if (tab === 'tasks' && parts.length === 1) {
      const { mountTaskDetail } = await import('./task-detail.js');
      if (!current()) return;
      if (creatingTask && !data.can_create) { body.innerHTML = empty('当前账号无权创建任务。'); return; }
      await mountTaskDetail(body, { creating: creatingTask, capabilities: data, task: creatingTask ? {title:'',description:'',status:'todo',version:0,owner_member_id:data.member_id} : data, project: project?.name || id, route, request, signal,
        back: taskViews.get(id) || base + '/tasks', navigation });
      return;
    }
    body.innerHTML =
      (header.status === 'rejected' || directory.status === 'rejected' || partial(data) ? empty('部分数据暂不可用。') : '') +
      (botSettings ? link(base + '/bot', '返回 Bot 对话') : '') +
      contents(contentRoute, data,
        new Map((directory.value?.data?.members || []).map(m => [m.member_id || m.id, m.display_name])));
    if (tab === 'members') wireProjectMembers(body,{data,projectId:id,request,signal,navigation,refresh});
    const settings = body.querySelector('[data-project-settings]');
    if (settings) settings.onclick = () => openProfile(settings, workspace.project_types, project);
    wireFilters(body, route);
    const check = body.querySelector('[data-check]');
    if (check)
      check.onclick = async () => {
        const result = body.querySelector('[data-result]');
        check.disabled = true;
        result.textContent = '正在验证接入…';
        const checks = {
          available: '可用',
          not_bound: '待绑定',
          bot_member: '机器人已入群',
          bot_not_member: '机器人未入群',
          member: '账号与机器人均已入群',
          active: '有效',
          allowed: '允许',
          denied: '无权使用',
          project_only: '仅项目内',
          account_source_denied: '账号无群访问权',
          inactive_or_missing: '账号未启用',
          not_checked: '未验证',
        };
        try {
          const response = await request('/api/v1/projects/' + id + '/feishu/check', {
            method: 'POST',
            body: { action: 'project.read', public_scope: 'project_only' },
            signal,
          });
          if (current())
            result.innerHTML = facts(
              [
                ['机器人', response.bot],
                ['目标群', response.group],
                ['账号', response.account],
                ['项目权限', response.action],
                ['公开范围', response.public_scope],
              ].map(([name, state]) => [name, checks[state] || label(state)]),
            );
        } catch (error) {
          if (current()) result.textContent = failure(error);
        } finally {
          if (current()) check.disabled = false;
        }
      };
  } catch (error) {
    if (!current()) return;
    body.innerHTML = empty(failure(error)) + '<button class="button" type="button">重新加载</button>';
    body.querySelector('button').onclick = () => {
      mountProjects(host, route, request, { signal, memory, scope, navigation });
    };
  } finally {
    body.removeAttribute('aria-busy');
  }
}
export { label as projectStatusLabel };
