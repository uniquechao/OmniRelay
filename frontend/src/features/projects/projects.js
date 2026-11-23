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
export const identifier = /^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$/;
export const sourceKinds = ['minutes', 'document', 'sheets', 'bitable', 'file', 'message'];
export const projectStatuses = ['planned', 'active', 'paused', 'blocked', 'completed', 'archived'];

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
export const withQuery = (path, params) => path + (params.size ? '?' + params : '');
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

export function pageNumber(params) {
  const page = Number(params.get('page') || 1);
  return Number.isInteger(page) && page > 0 && page <= 100000 ? page : 1;
}

export function briefingState(row) {
  if (row.delivery_state === 'success' && row.delivery_confirmed_at) return '已送达';
  return (
    { not_sent: '尚未发送', pending: '投递中', failed: '投递失败', withheld: '回执受限' }[
      row.delivery_state
    ] || '送达待确认'
  );
}
export const projectFollowsChanged = 'omnirelay:project-follows-changed';

function endpoint(route) {
  const { id, tab, parts, params } = route;
  const root = '/api/v1/projects';
  const query = new URLSearchParams();
  if (['meetings', 'reports'].includes(tab) && !parts.length) {
    query.set('page', pageNumber(params));
    query.set('page_size', 25);
  }
  if (tab !== 'members' && params.get('q')) query.set('q', params.get('q'));
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
export function failure(error) {
  if ([403, 404].includes(error.status)) return '当前项目或来源已不可访问。';
  if (error.status === 409) return '数据或权限已变化，请重新加载。';
  return '暂时无法加载，请重试。';
}
export {tabs as projectTabs, label as projectStatusLabel};

export async function loadProjectWorkspace(route, request, signal) {
  const {id,tab,parts,params}=route;
  const base='/api/v1/projects/'+id;
  const read=path=>request(path,{signal});
  const overview=read(base+'/overview');
  const board=tab==='tasks' && !parts.length;
  const contentRoute=tab==='bot' && params.get('view')==='connection'?{...route,tab:'feishu'}:route;
  function readContent() {
    if(tab==='overview')return overview;
    if(['bot','docs','chats'].includes(contentRoute.tab))return;
    if(board)return read(base+'/board?limit=1');
    if(tab==='tasks' && parts[0]==='new')return read(base+'/tasks');
    return read(endpoint(contentRoute));
  }
  const [header,result,directory]=await Promise.allSettled([
    overview,readContent(),board || tab==='overview'?read(base+'/members'+(board?'?purpose=assignment':'')):undefined,
  ]);
  if(header.status==='rejected' && [403,404].includes(header.reason.status))throw header.reason;
  if(result.status==='rejected')throw result.reason;
  if(board && directory.status==='rejected')throw directory.reason;
  const data=result.value?.data ?? result.value;
  return {data,contentRoute,workspace:header.value?.data.workspace,milestones:header.value?.data.milestones?.items || [],
    directory:directory.value?.data?.members || [],partial:header.status==='rejected' || directory.status==='rejected' || Boolean(data && partial(data))};
}
