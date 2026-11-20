import { chatAPI, mountChatReader } from './chat-reader.js';

const tabs = {
  overview: '项目概览',
  members: '成员',
  chats: '群聊',
  meetings: '会议纪要',
  docs: '文档',
  reports: '日报',
  tasks: '任务',
  feishu: '飞书 Bot',
};
const identifier = /^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$/;
const sourceKinds = ['minutes', 'document', 'sheets', 'bitable', 'file', 'message'];
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
const panel = (title, body) =>
  `<section class="panel"><header class="section-head"><h2>${esc(title)}</h2></header>${body}</section>`;
const facts = (rows) =>
  '<dl class="facts">' +
  rows.map(([name, value]) => `<div><dt>${esc(name)}</dt><dd>${esc(value ?? '暂无')}</dd></div>`).join('') +
  '</dl>';
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
export function projectRoute(hash) {
  if (!hash || !/^#\/projects(?:[/?]|$)/.test(hash)) return;
  const [path, query = ''] = hash.split('?');
  const [, area, id, tab = 'overview', ...parts] = path.split('/');
  const params = new URLSearchParams(query);
  if (!id && path === '#/projects') return { hash: withQuery(path, params), params };
  if (!identifier.test(id || '') || !parts.every((s) => identifier.test(s))) return;
  const base = '#/' + area + '/' + id;
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
  const statuses = ['planned', 'active', 'paused', 'blocked', 'completed', 'archived'];
  return (
    `<form class="toolbar" data-filters><input type="search" class="search" name="q" aria-label="搜索" placeholder="${directory ? '搜索项目' : '搜索记录'}" maxlength="200" value="${esc(q)}">` +
    (directory
      ? '<select class="select" name="status" aria-label="项目状态"><option value="">全部状态</option>' +
        statuses
          .map(
            (s) =>
              `<option value="${s}"${s === route.params.get('status') ? ' selected' : ''}>${label(s)}</option>`,
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
      else if (value.trim()) params.set(key, value.trim());
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
function directoryRows(items) {
  return table(
    ['项目名称', '类型', '阶段'],
    items.map((row) => [
      link('#/projects/' + row.id + '/overview', row.name),
      esc(row.project_type),
      label(row.status),
    ]),
    '没有符合条件的项目。',
  );
}
export async function mountProjectSummary(host, request, signal) {
  window.OmniRelayLoading.mount(host, { size: 'page', label: '正在加载项目…' });
  try {
    const data = await request('/api/v1/projects?page=1&page_size=10', { signal });
    if (!signal.aborted) host.innerHTML = panel('项目列表', directoryRows(data.items));
  } catch (error) {
    if (!signal.aborted) host.innerHTML = empty(failure(error));
  } finally {
    host.removeAttribute('aria-busy');
  }
}

const taskStages = { todo: '待开始', in_progress: '进行中', blocked: '受阻', done: '已完成' };
const priorityStyles = { P0: 'urgent', P1: 'important', P2: 'normal', P3: 'low' };
const priorityTones = { P0: 'danger', P1: 'important' };

function taskDeadline(row) {
  if (!row?.due_at) return '<span class="pr-deadline">无截止日期</span>';
  const warning = row.due_alert ? (row.due_alert_label || '临期告警') : '';
  const title = [warning, row.due_at].filter(Boolean).join(' · ');
  return `<time title="${esc(title)}" class="pr-deadline" datetime="${esc(row.due_at)}"${row.due_alert ? ' data-deadline-alert' : ''}>${row.due_alert ? '<span aria-hidden="true"><svg class="icon"><use href="#status-warning"/></svg></span><span class="sr-only">' + esc(warning) + ' · </span>' : ''}${esc(row.due_label || row.due_at)}</time>`;
}

function taskFilters(route, board, nodes, people) {
  const choice = (value, title, selected) =>
    `<option value="${esc(value)}"${value === selected ? ' selected' : ''}>${esc(title)}</option>`;
  const owner = route.params.get('owner_member_id') || '';
  const milestone = route.params.get('milestone_unassigned') === 'true' ? '@none' : route.params.get('milestone_id') || '';
  const owners = new Set(board.owner_member_ids || []);
  if (owner) owners.add(owner);
  if (milestone && milestone !== '@none' && !nodes.has(milestone)) nodes.set(milestone, milestone);
  return filters(route, false,
    '<select class="select" name="owner_member_id" aria-label="筛选负责人">' + choice('', '全部负责人', owner) +
    [...owners].map((id) => choice(id, people.get(id) || id, owner)).join('') + '</select>' +
    '<select class="select" name="milestone_id" aria-label="筛选里程碑">' + choice('', '全部里程碑', milestone) +
    [...nodes].map(([id,title]) => choice(id,title,milestone)).join('') + choice('@none','未关联里程碑',milestone) + '</select>');
}

function taskRows(base, items, nodes = new Map(), people = new Map(), canMove = false) {
  return table(
    ['任务', '状态', '优先级', '负责人', '里程碑', '期限'],
    items.map((row) => [
      link(base + '/tasks/' + row.id, row.title),
      (row.can_move ?? canMove) && row.status !== 'cancelled'
        ? `<select class="select" data-move-task="${esc(row.id)}" aria-label="移动任务：${esc(row.title)}">${Object.entries(taskStages).map(([id, name]) =>
          `<option value="${id}"${id === row.column_id ? ' selected' : ''}>${name}</option>`).join('')}</select>`
        : esc(taskStages[row.status] || label(row.status)),
      esc(row.priority_label || '未设置'),
      esc(people.get(row.owner_member_id) || row.owner_member_id || '未分配'),
      esc(nodes.get(row.milestone_id) || row.milestone_id || '未关联'),
      taskDeadline(row),
    ]),
    '暂无任务。',
  );
}

function taskCard(base, row, nodes, people, canMove) {
  const owner = people.get(row.owner_member_id) || row.owner_member_id || '未分配';
  const milestone = nodes.get(row.milestone_id) || row.milestone_id;
  const tags = row.tag_ids || [];
  const movable = (row.can_move ?? canMove) && row.status !== 'cancelled';
  return `<li data-task-id="${esc(row.id)}" draggable="${movable}">
    <a href="${esc(base + '/tasks/' + row.id)}" class="pr-task" draggable="false" data-priority="${priorityStyles[row.priority] || 'normal'}"
       ${movable ? 'aria-keyshortcuts="Alt+ArrowLeft Alt+ArrowRight" aria-describedby="task-move-help"' : ''}>
      <span class="pr-task-top"><span class="pr-task-code" title="${esc(row.id)}">${esc(row.id)}</span>
        <span class="pr-task-person" title="${esc(owner)}">${window.OmniRelayUI.personAvatar(owner, row.owner_member_id || owner)}<span class="sr-only">负责人：${esc(owner)}</span></span></span>
      <span class="pr-task-tags"><span data-tone="${priorityTones[row.priority] || 'pending'}">${esc(row.priority_label || '未设置')}</span>
        ${tags.slice(0, 2).map(tag => '<span>' + esc(tag) + '</span>').join('')}${tags.length > 2 ? '<span>+' + (tags.length - 2) + '</span>' : ''}</span>
      <strong class="pr-task-title" title="${esc(row.title)}">${esc(row.title)}</strong>
      ${row.status === 'blocked' && row.blocked_reason ? `<span class="pr-task-blocker" title="${esc(row.blocked_reason)}">${esc(row.blocked_reason)}</span>` : ''}
      ${milestone ? `<span class="pr-task-milestone" title="里程碑：${esc(milestone)}"><svg class="icon" aria-hidden="true"><use href="#status-milestone"/></svg><span><span class="sr-only">里程碑：</span>${esc(milestone)}</span></span>` : ''}
      ${taskDeadline(row)}
    </a></li>`;
}

function firstTaskPage(route) {
  const params = new URLSearchParams(route.params);
  params.delete('cursor');
  params.delete('column_id');
  return withQuery(route.hash.split('?')[0], params);
}

function requestMoveReason(task, signal, reopening) {
  return new Promise(resolve => {
    let reason;
    const close = () => dialog.close();
    const dialog = window.OmniRelayUI.openDialog({
      title: reopening ? '重新打开任务' : '移至受阻', description: task.title, variant: 'form', confirm: '保存',
      body: '<label>' + (reopening ? '变更原因' : '受阻原因') + '<textarea class="input" name="reason" required autofocus></textarea></label>',
      onSubmit(form) {
        const field = form.elements.reason;
        if (!field.value.trim()) return { field, message: reopening ? '请填写变更原因。' : '请填写受阻原因。' };
        reason = field.value.trim();
      },
      onClose(confirmed) {
        signal.removeEventListener('abort', close);
        resolve(confirmed ? reason : undefined);
      },
    });
    signal.addEventListener('abort', close, { once: true });
  });
}

function wireMoves(body, route, board, request, signal, reload) {
  const tasks = new Map(board.columns.flatMap(c => c.items.map(t => [t.id, t])));
  const current = () => !signal.aborted && location.hash === route.hash && body.isConnected;
  let dragged;
  let busy = false;
  function refresh() {
    const first = firstTaskPage(route);
    if (first !== route.hash) location.hash = first;
    else return reload();
  }
  async function move(id, target, before) {
    const task = tasks.get(id);
    if (!current() || busy || !task || task.can_move === false || task.status === 'cancelled' || !Object.hasOwn(taskStages, target) ||
        before === id || (!before && target === task.column_id)) return;
    busy = true;
    let reason = task.blocked_reason || '';
    if (target === 'blocked' && task.column_id !== target) {
      reason = await requestMoveReason(task, signal, false);
      if (reason === undefined || !current()) {
        busy = false;
        return;
      }
    }
    let changeReason = '';
    if (task.status === 'done' && target !== task.column_id) {
      changeReason = await requestMoveReason(task, signal, true);
      if (!changeReason || !current()) {
        busy = false;
        return;
      }
    }
    body.setAttribute('aria-busy', 'true');
    try {
      await request('/api/v1/projects/' + route.id + '/tasks/' + id + '/move', {
        method: 'POST', signal,
        body: { request_key: crypto.randomUUID(), expected_task_version: task.version,
          expected_board_version: board.version, target_column_id: target, before_id: before,
          blocked_reason: target === 'blocked' ? reason : '', change_reason: changeReason },
      });
    } catch (error) {
      if (!current()) return;
      const message = error.status === 409 ? '任务或看板已更新，请重新加载后再移动。' :
        [403, 404].includes(error.status) ? '移动权限或任务已变化，请重新加载。' : '未能确认移动结果，请重新加载。';
      body.innerHTML = empty(message) + '<button class="button" type="button">重新加载</button>';
      body.removeAttribute('aria-busy');
      const retry = body.querySelector('button');
      retry.onclick = refresh;
      retry.focus();
      return;
    }
    if (!current()) return;
    await refresh();
    if (signal.aborted || location.hash !== route.hash) return;
    const panel = document.getElementById('project-panel');
    const key = CSS.escape(id);
    panel?.querySelector(`[data-task-id="${key}"] a, [data-move-task="${key}"]`)?.focus();
  }
  body.onchange = e => {
    if (!e.target.matches('[data-move-task]')) return;
    const id = e.target.dataset.moveTask;
    const target = e.target.value;
    e.target.value = tasks.get(id).column_id;
    void move(id, target);
  };
  body.onkeydown = e => {
    const card = e.target.closest('[data-task-id][draggable="true"]');
    if (!card || !e.altKey || !['ArrowLeft', 'ArrowRight'].includes(e.key)) return;
    e.preventDefault();
    const stages = Object.keys(taskStages);
    const index = stages.indexOf(card.closest('[data-column]').dataset.column);
    void move(card.dataset.taskId, stages[index + (e.key === 'ArrowRight' ? 1 : -1)]);
  };
  function highlight(lane) {
    body.querySelector('[data-drop-active]')?.removeAttribute('data-drop-active');
    lane?.setAttribute('data-drop-active', '');
  }
  body.ondragstart = e => {
    const card = e.target.closest('[data-task-id][draggable="true"]');
    if (!card || busy) return e.preventDefault();
    dragged = card.dataset.taskId;
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', dragged);
  };
  body.ondragover = e => {
    const lane = e.target.closest('[data-column]');
    if (!dragged || !lane) return;
    e.preventDefault();
    highlight(lane);
  };
  body.ondrop = e => {
    const lane = e.target.closest('[data-column]');
    if (!dragged || !lane) return;
    e.preventDefault();
    void move(dragged, lane.dataset.column, e.target.closest('[data-task-id]')?.dataset.taskId);
    dragged = undefined;
    highlight();
  };
  body.ondragend = () => { dragged = undefined; highlight(); };
}

function columnNext(route, column) {
  if (!column.next_cursor) return '';
  const params = new URLSearchParams(route.params);
  params.set('column_id', column.id);
  params.set('cursor', column.next_cursor);
  return link(withQuery(route.hash.split('?')[0], params), '查看「' + column.name + '」下一页');
}
function contents(route, data, milestones = [], people = new Map()) {
  const { base, tab, parts } = route;
  if (tab === 'overview') {
    const { workspace, milestones } = data;
    const project = workspace.project;
    return (
      panel(
        '项目概况',
        facts([
          ['负责人', workspace.owner?.display_name],
          ['项目类型', project.project_type],
          ['进展速览', milestones.progress?.record?.title || project.background],
        ]),
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
              esc(node.owner_name || node.owner_member_id || '—'),
              label(node.status),
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
            .map((node) => [esc(node.title), esc(node.owner_name || node.owner_member_id || '—')]),
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
          ['成员', '角色', '职责'],
          data.members
            .filter((row) =>
              [row.display_name, row.role_title, row.responsibility].some((value) =>
                String(value ?? '').includes(route.params.get('q') || ''),
              ),
            )
            .map((row) => [esc(row.display_name), esc(row.role_title), esc(row.responsibility)]),
          '暂无项目成员。',
        ),
    );
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
                  ['可见提交数', row.visible_submissions],
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
  if (tab === 'tasks') {
    if (parts.length === 3) return panel('关联任务', taskRows(base, data.items));
    const list = parts[0] === 'list';
    const criteria = new URLSearchParams(route.params);
    criteria.delete('cursor');
    criteria.delete('column_id');
    const nodes = new Map(milestones.filter(m => m.kind === 'milestone' && m.id).map(m => [m.id, m.title]));
    const views = [['看板', base + '/tasks'], ['列表', base + '/tasks/list']];
    const nav = '<nav class="pr-segment" aria-label="任务视图">' + views.map(([name, path]) =>
      `<a href="${esc(withQuery(path, criteria))}"${route.hash.split('?')[0] === path ? ' aria-current="page"' : ''}>${name}</a>`).join('') + '</nav>';
    const toolbar = '<div class="pr-toolbar task-toolbar">' + taskFilters(route, data, nodes, people) + nav + '</div>';
    if (list) return toolbar + taskRows(base, data.columns.flatMap(c => c.items), nodes, people, data.can_move) +
      data.columns.map(c => columnNext(route, c)).join(' ');
    const tones = { todo: 'pending', in_progress: 'progress', blocked: 'danger', done: 'success' };
    return toolbar + (data.can_move ? '<small id="task-move-help">拖动卡片可移动任务，键盘可用 Alt + 左右方向键移列。</small>' : '') +
      '<div class="pr-board" role="region" aria-label="任务看板" tabindex="0">' + data.columns.map(c =>
        `<section class="pr-lane" data-column="${esc(c.id)}"><h3><span class="chip" data-tone="${tones[c.id] || 'pending'}">${esc(c.name)}</span><span>${esc(c.total)}</span></h3>
        <ul>${c.items.map(row => taskCard(base, row, nodes, people, data.can_move)).join('')}</ul>
        ${c.items.length ? '' : '<p class="pr-lane-empty">暂无任务</p>'}${columnNext(route, c)}</section>`).join('') + '</div>';
  }
  const rows = data.items.map((row) => {
    const kind = row.ref?.kind || row.resource_type,
      id = row.ref?.source_id || row.resource_token;
    return [
      esc(row.title || '未命名'),
      esc(row.editor || row.created_at || '—'),
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
  if (!id || (['docs', 'meetings', 'reports'].includes(tab) && !parts.length)) {
    query.set('page', pageNumber(params));
    query.set('page_size', 25);
  }
  if (tab === 'tasks' && (!parts.length || parts[0] === 'list')) {
    for (const key of ['cursor', 'column_id', 'owner_member_id', 'milestone_id', 'milestone_unassigned']) if (params.has(key)) query.set(key, params.get(key));
  }
  if (tab !== 'members' && params.get('q')) query.set('q', params.get('q'));
  if (!id) {
    if (params.get('status')) query.set('status', params.get('status'));
    return withQuery(root, query);
  }
  let path = root + '/' + id + '/' + tab;
  if (tab === 'reports' && parts.length) path += '/briefings' + (parts[1] ? '/' + parts[1] : '');
  if (tab === 'tasks' && parts.length === 3) {
    path = root + '/' + id + '/sources/' + parts[1] + '/' + parts[2] + '/tasks';
    if (parts[1] === 'message') query.set('chat_id', params.get('chat_id'));
  } else if (tab === 'tasks' && parts.length && parts[0] !== 'list') path += '/' + parts[0];
  return withQuery(path, query);
}

async function fetchTaskBoard(route, request, signal) {
  const page = request(endpoint(route), { signal });
  if (!route.params.has('column_id')) return page;
  // 列游标只读取一列，其余列沿用同一看板版本的首页。
  const first = projectRoute(firstTaskPage(route));
  const [selected, all] = await Promise.all([page, request(endpoint(first), { signal })]);
  if (selected.data.version !== all.data.version) {
    throw Object.assign(new Error('看板版本已变化'), { status: 409 });
  }
  const column = selected.data.columns[0];
  all.data.columns = all.data.columns.map(c => c.id === column.id ? column : c);
  return all;
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
export async function mountProjects(host, route, request, { signal, memory, userId, navigation }) {
  const { id, base, tab, parts } = route;
  const loading = (element) => window.OmniRelayLoading.mount(element, { size: 'page', label: '正在加载…' });
  const current = () => !signal.aborted && location.hash === route.hash;
  const saved = history.state?.projectList;
  const savedRoute = typeof saved?.list === 'string' ? projectRoute(saved.list) : undefined;
  if (saved?.userId === userId && savedRoute && !savedRoute.id) {
    Object.assign(memory, {
      list: saved.list,
      focus: saved.focus,
      scroll: saved.scroll,
      windowY: saved.windowY,
    });
  }
  const taskViews = memory.taskViews ??= new Map();
  if (id && tab === 'tasks' && (!parts.length || parts[0] === 'list')) {
    taskViews.set(id, firstTaskPage(route));
  }
  const back = memory.list || '#/projects';
  if (id && memory.list)
    history.replaceState(
      {
        ...history.state,
        projectList: {
          userId,
          list: memory.list,
          focus: memory.focus,
          scroll: memory.scroll,
          windowY: memory.windowY,
        },
      },
      '',
    );
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
      body.innerHTML = directoryRows(data.items) + pager(route, data.total, 25);
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
    const boardView = tab === 'tasks' && (!parts.length || parts[0] === 'list');
    const content =
      tab === 'overview'
        ? overview
        : tab === 'chats'
          ? Promise.resolve()
          : boardView ? fetchTaskBoard(route, request, signal) : request(endpoint(route), { signal });
    const members = boardView ? request('/api/v1/projects/' + id + '/members', { signal }) : undefined;
    const [header, result, directory] = await Promise.allSettled([overview, content, members]);
    if (!current()) return;
    if (header.status === 'rejected' && [403, 404].includes(header.reason.status)) throw header.reason;
    if (result.status === 'rejected') throw result.reason;
    const project = header.value?.data.workspace.project;
    if (project) {
      const title = host.querySelector('#page-heading');
      title.textContent = project.name || project.id;
      if (project.project_type)
        title.insertAdjacentHTML('beforebegin', `<span class="kind">${esc(project.project_type)}</span>`);
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
    if (tab === 'chats') {
      let chatHash = route.hash;
      const reader = chatAPI(request, '/api/v1/projects/' + id + '/chat-reader');
      delete reader.refresh;
      mountChatReader(body, reader, {
        signal,
        project: true,
        projectId: id,
        draftScope: userId,
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
    if (tab === 'tasks' && parts.length === 1 && parts[0] !== 'list') {
      const { mountTaskDetail } = await import('./task-detail.js');
      if (!current()) return;
      await mountTaskDetail(body, { task: data, project: project?.name || id, route, request, signal,
        back: taskViews.get(id) || base + '/tasks', navigation });
      return;
    }
    body.innerHTML =
      (header.status === 'rejected' || directory.status === 'rejected' || partial(data) ? empty('部分数据暂不可用。') : '') +
      contents(route, data, header.value?.data.milestones?.items || [],
        new Map((directory.value?.data?.members || []).map(m => [m.member_id || m.id, m.display_name])));
    if (boardView && data.can_move) wireMoves(body, route, data, request, signal, () =>
      mountProjects(host, route, request, { signal, memory, userId, navigation }));
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
          identity_unavailable: '账号未关联',
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
      if (tab === 'tasks' && route.params.has('column_id')) location.hash = firstTaskPage(route);
      else mountProjects(host, route, request, { signal, memory, userId, navigation });
    };
  } finally {
    body.removeAttribute('aria-busy');
  }
}
