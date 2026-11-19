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

function filters(route, directory) {
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
    '<button class="button">查询</button></form>'
  );
}

function wireFilters(host, route) {
  host.querySelector('[data-filters]')?.addEventListener('submit', (event) => {
    event.preventDefault();
    const params = new URLSearchParams();
    for (const [key, value] of new FormData(event.currentTarget))
      if (value.trim()) params.set(key, value.trim());
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

function taskRows(base, items) {
  return table(
    ['任务', '状态', '期限'],
    items.map((row) => [
      link(base + '/tasks/' + row.id, row.title),
      label(row.status),
      esc(row.due_at || '—'),
    ]),
    '暂无任务。',
  );
}

function columnNext(route, column) {
  if (!column.next_cursor) return '';
  const params = new URLSearchParams(route.params);
  params.set('column_id', column.id);
  params.set('cursor', column.next_cursor);
  return link(withQuery(route.hash.split('?')[0], params), '查看「' + column.name + '」下一页');
}
function contents(route, data) {
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
              node.task_id ? link(base + '/tasks/' + node.task_id, node.title) : esc(node.title),
              esc(node.owner_name || '—'),
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
            .map((node) => [esc(node.title), esc(node.owner_name || '—')]),
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
    if (parts.length && parts[0] !== 'list')
      return panel(
        data.title,
        facts([
          ['状态', label(data.status)],
          ['期限', data.due_at],
          ['任务说明', data.description],
        ]) + link(base + '/tasks', '返回任务看板'),
      );
    const list = parts[0] === 'list';
    return (
      '<nav class="pr-segment" aria-label="任务视图">' +
      link(base + '/tasks', '看板') +
      link(base + '/tasks/list', '列表') +
      '</nav>' +
      filters(route, false) +
      (list
        ? taskRows(
            base,
            data.columns.flatMap((c) => c.items),
          ) + data.columns.map((c) => columnNext(route, c)).join(' ')
        : '<div class="pr-board">' +
          data.columns
            .map(
              (c) =>
                `<section class="pr-lane"><h3>${esc(c.name)} · ${esc(c.total)}</h3><ul>${c.items.map((row) => `<li class="pr-task">${link(base + '/tasks/' + row.id, row.title)}</li>`).join('')}</ul>${c.items.length ? '' : empty('暂无任务。')}${columnNext(route, c)}</section>`,
            )
            .join('') +
          '</div>')
    );
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
    for (const key of ['cursor', 'column_id']) if (params.has(key)) query.set(key, params.get(key));
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
export async function mountProjects(host, route, request, { signal, memory, userId }) {
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
      location.hash = base + '/' + next.dataset.tab;
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
    const content =
      tab === 'overview'
        ? overview
        : tab === 'chats'
          ? Promise.resolve()
          : request(endpoint(route), { signal });
    const [header, result] = await Promise.allSettled([overview, content]);
    if (!current()) return;
    if (header.status === 'rejected' && [403, 404].includes(header.reason.status)) throw header.reason;
    if (result.status === 'rejected') throw result.reason;
    const project = header.value?.data.workspace.project;
    if (project) {
      const title = host.querySelector('#page-heading');
      title.textContent = project.name;
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
    body.innerHTML =
      (header.status === 'rejected' || partial(data) ? empty('部分数据暂不可用。') : '') +
      contents(route, data);
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
    body.querySelector('button').onclick = () =>
      mountProjects(host, route, request, { signal, memory, userId });
  } finally {
    body.removeAttribute('aria-busy');
  }
}
