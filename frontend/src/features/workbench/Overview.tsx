import {useLayoutEffect, useRef} from 'react';
import {Button, Icon, Loading, Panel, Status} from '../../ui/components/Elements';
import {client} from '../../api/session';
import {useQueries, useQuery, type UseQueryResult} from '@tanstack/react-query';
import type {Route} from '../../routes/targets';
import {projectStatusLabel} from '../../../../web/projects.js';
import '../../../../design/prototype/omni-relay-overview.css';

type Count = {total: number};
type Project = {id: string; name: string; project_type: string; status: string};
type ProjectPage = Count & {items: Project[]};
type Metric = {route: string; label: string; detail: string; icon: string; endpoint: string};

const projectLimit = 10;
const number = new Intl.NumberFormat('zh-CN');
const projectMetric = {
  route: 'projects', label: '项目总数', detail: '当前可访问的项目', icon: 'nav-projects',
  endpoint: '/api/v1/projects?page=1&page_size=' + projectLimit,
};
const otherMetrics: Metric[] = [
  {route: 'bot-chats', label: '归档消息', detail: '可查看且未撤回的消息', icon: 'nav-bot-chat', endpoint: '/api/v1/feishu/chats/messages/count'},
  {route: 'drive', label: '文件总数', detail: '当前可见的网盘文件', icon: 'file-grid', endpoint: '/api/v1/feishu/files?limit=1'},
  {route: 'users', label: '启用用户', detail: '已启用的工作台账号', icon: 'nav-team', endpoint: '/api/v1/system/user-page?limit=1&offset=0&status=active'},
];
const queryOptions = {
  retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false,
};
const stageTone: Record<string, 'progress' | 'success' | 'danger' | 'warning'> = {
  active: 'progress', completed: 'success', blocked: 'danger', paused: 'warning',
};

async function readTotal<T extends Count>(endpoint: string, signal: AbortSignal): Promise<T> {
  const data = await client.bind()(endpoint, {signal}) as T;
  if (!Number.isSafeInteger(data?.total) || data.total < 0) throw new Error('总览统计响应无效。');
  return data;
}

function MetricLink({metric, href, result}: {metric: Metric; href: string; result: UseQueryResult<Count>}) {
  const loading = result.isPending || result.isFetching;
  return <a className="overview-metric" href={href}>
    <span className="overview-metric-label">
      <Icon name={metric.icon}/>{metric.label}<Icon name="action-expand" className="icon overview-chevron"/>
    </span>
    <span className="overview-metric-value" data-state={loading || result.isError ? 'status' : 'value'} aria-live="polite">
      {loading ? '加载中…' : result.isError ? '暂不可用' : number.format(result.data.total)}
    </span>
    <span className="aux">{metric.detail}</span>
  </a>;
}

export function Overview({links}: {links: Route[]}) {
  const heading = useRef<HTMLHeadingElement>(null);
  useLayoutEffect(() => heading.current?.focus({preventScroll: true}), []);
  const projects = links.find(route => route.id === projectMetric.route);
  const shortcuts = links.filter(route => route.group === '系统管理');
  const metrics = otherMetrics.flatMap(metric => {
    const link = links.find(route => route.id === metric.route);
    return link ? [{metric, href: link.path.slice(1)}] : [];
  });
  const projectResult = useQuery({
    ...queryOptions, queryKey: ['workbench', projectMetric.route], enabled: !!projects,
    queryFn: async ({signal}) => {
      const page = await readTotal<ProjectPage>(projectMetric.endpoint, signal);
      if (!Array.isArray(page.items)) throw new Error('总览项目列表响应无效。');
      return page;
    },
  });
  const counts = useQueries({queries: metrics.map(({metric}) => ({
    ...queryOptions, queryKey: ['workbench', metric.route],
    queryFn: async ({signal}: {signal: AbortSignal}) => {
      const {total} = await readTotal(metric.endpoint, signal);
      return {total};
    },
  }))});
  const fetching = projectResult.isFetching || counts.some(result => result.isFetching);
  const failed = projectResult.isError || counts.some(result => result.isError);
  const projectHref = projects?.path.slice(1);

  function refresh() {
    if (fetching) return;
    if (projects) void projectResult.refetch();
    for (const result of counts) void result.refetch();
  }

  return <div className="overview">
    <header className="head overview-heading">
      <h1 id="page-heading" tabIndex={-1} ref={heading}>工作台总览</h1>
      <Button className="overview-refresh" onClick={refresh} aria-disabled={fetching}>
        <Icon name="chat-refresh"/>刷新数据
      </Button>
    </header>
    <section className="overview-metrics" aria-label="工作台统计">
      {projectHref && <MetricLink metric={projectMetric} href={projectHref} result={projectResult}/>}
      {metrics.map(({metric, href}, index) =>
        <MetricLink key={metric.route} metric={metric} href={href} result={counts[index]}/>)}
    </section>
    {failed && <p className="overview-feedback aux" role="status">部分数据加载失败，可刷新数据重试。</p>}
    <div className="overview-content" data-has-shortcuts={shortcuts.length > 0}>
      <section className="panel" aria-labelledby="overview-projects">
        <header className="section-head">
          <h2 id="overview-projects">项目速览</h2>
          {projectHref && <a className="button primary" href={projectHref}>查看项目列表</a>}
        </header>
        {projectResult.isPending || projectResult.isFetching ? <Loading size="compact" label="正在加载项目…"/>
          : projectResult.isError ? <p className="aux">暂时无法加载项目。</p>
          : projectResult.data.items.length === 0 ? <p className="aux">当前没有可访问的项目。</p>
          : <>
            <div className="table-box overview-project-table">
              <table aria-labelledby="overview-projects">
                <thead><tr>{['项目名称', '类型', '阶段'].map(label => <th key={label} scope="col">{label}</th>)}</tr></thead>
                <tbody>{projectResult.data.items.map(row => <tr key={row.id}>
                  <td><a href={'#/projects/' + encodeURIComponent(row.id) + '/overview'}>{row.name}</a></td>
                  <td>{row.project_type || '待确认'}</td>
                  <td><Status tone={stageTone[row.status] ?? 'pending'}>{projectStatusLabel(row.status)}</Status></td>
                </tr>)}</tbody>
              </table>
            </div>
            {projectResult.data.total > projectResult.data.items.length &&
              <p className="aux overview-project-note">显示前 {projectResult.data.items.length} 个项目</p>}
          </>}
      </section>
      {shortcuts.length > 0 && <Panel title="系统管理">
        <ul className="overview-shortcuts">
          {shortcuts.map(route => <li key={route.id}>
            <a href={route.path.slice(1)}>{route.title}<Icon name="action-expand" className="icon overview-chevron"/></a>
          </li>)}
        </ul>
      </Panel>}
    </div>
  </div>;
}
