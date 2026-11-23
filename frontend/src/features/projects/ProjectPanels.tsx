import { z } from '../../api/validation.ts'
import {
  overviewSchema,
  briefingSchema,
  reportSchema,
  sourceTaskSchema,
  meetingSchema,
  connectionSchema,
  type ProjectRequest,
  type Briefing,
  type ProjectType,
} from './project-contracts'
import type { ProjectRoute } from './projects'
import type { ReactNode } from 'react'
import { useState } from 'react'
import {
  briefingState,
  failure,
  identifier,
  pageNumber,
  projectTabs,
  projectStatusLabel as label,
  sourceKinds,
  withQuery,
} from './projects'

const taskStages: Record<string, string> = {
  todo: '待开始',
  in_progress: '进行中',
  blocked: '受阻',
  done: '已完成',
}
const checkFieldLabels: Record<string, string> = {
  bot: '机器人',
  group: '目标群',
  account: '账号',
  action: '项目权限',
  public_scope: '公开范围',
}
const checkLabels: Record<string, string> = {
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
}
function Panel({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="panel">
      <header className="section-head">
        <h2>{title}</h2>
        {action}
      </header>
      {children}
    </section>
  )
}
function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="facts">
      {rows.map(([name, value]) => (
        <div key={name}>
          <dt>{name}</dt>
          <dd>{value ?? '暂无'}</dd>
        </div>
      ))}
    </dl>
  )
}
function Table<T extends { id?: string }>({
  headers,
  items,
  cells,
  empty,
}: {
  headers: string[]
  items: T[]
  cells: (row: T) => ReactNode[]
  empty: string
}) {
  if (!items.length)
    return (
      <p className="empty" role="status">
        {empty}
      </p>
    )
  return (
    <div className="table-box">
      <table>
        <thead>
          <tr>
            {headers.map((h) => (
              <th scope="col" key={h}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((item, index) => (
            <tr key={item.id || index}>
              {cells(item).map((cell, i) => (
                <td key={headers[i]}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
export function TypeBadge({ type, types }: { type: string; types: ProjectType[] }) {
  return type ? (
    <span className="kind" data-project-type-tone={types?.find((t) => t.name === type)?.tone || 'neutral'}>
      {type}
    </span>
  ) : (
    '—'
  )
}
function Deadline({
  row,
}: {
  row: Pick<z.infer<typeof sourceTaskSchema>, 'due_at' | 'due_alert' | 'due_alert_label' | 'due_label'>
}) {
  if (!row?.due_at) return <span className="pr-deadline">无截止日期</span>
  const warning = row.due_alert && (row.due_alert_label || '临期告警')
  return (
    <time
      title={[warning, row.due_at].filter(Boolean).join(' · ')}
      className="pr-deadline"
      dateTime={row.due_at}
      data-deadline-alert={warning ? '' : undefined}
    >
      {warning && (
        <>
          <svg aria-hidden="true" className="icon">
            <use href="#status-warning" />
          </svg>
          <span className="sr-only">{warning} · </span>
        </>
      )}
      {row.due_label || row.due_at}
    </time>
  )
}
function Search({ route }: { route: ProjectRoute }) {
  return (
    <form
      className="toolbar"
      onSubmit={(event) => {
        event.preventDefault()
        const q = String(new FormData(event.currentTarget).get('q') ?? '').trim()
        location.hash = withQuery(route.hash.split('?')[0], new URLSearchParams(q ? { q } : {}))
      }}
    >
      <input
        type="search"
        className="search"
        name="q"
        aria-label="搜索"
        placeholder="搜索记录"
        maxLength={200}
        defaultValue={route.params.get('q') || ''}
      />
      <button className="button">查询</button>
    </form>
  )
}
function Pager({ route, total }: { route: ProjectRoute; total: number }) {
  const page = pageNumber(route.params)
  const href = (n: number) => {
    const p = new URLSearchParams(route.params)
    p.set('page', String(n))
    return withQuery(route.hash.split('?')[0], p)
  }
  return (
    <nav className="pr-actions" aria-label="分页">
      {page > 1 && <a href={href(page - 1)}>上一页</a>}
      <span>
        第 {page} 页 · 共 {total} 条
      </span>
      {page * 25 < total && <a href={href(page + 1)}>下一页</a>}
    </nav>
  )
}
function Briefings({ base, items }: { base: string; items: Briefing[] }) {
  return (
    <Table
      headers={['项目简报', '生成状态', '投递状态', '确认时间']}
      items={items}
      empty="暂无项目简报。"
      cells={(row) => [
        <a href={base + '/reports/brief/' + row.id}>{row.business_date}</a>,
        row.generated_at ? '已生成' : '生成待确认',
        briefingState(row),
        row.delivery_confirmed_at || '—',
      ]}
    />
  )
}
function BriefList({ base, items, params }: { base: string; items: Briefing[]; params: URLSearchParams }) {
  if (!items.length)
    return (
      <p className="empty" role="status">
        暂无项目简报。
      </p>
    )
  return (
    <ul className="pr-records">
      {items.map((row) => (
        <li key={row.id}>
          <a className="pr-record pr-brief-link" href={withQuery(base + '/reports/brief/' + row.id, params)}>
            <span>
              <strong>{row.title || row.business_date + ' 项目简报'}</strong>
              <small>
                <time dateTime={row.business_date}>{row.business_date}</time>
                {row.generated_at && (
                  <>
                    {' '}
                    · <span>已生成</span>
                  </>
                )}
                {row.delivery_state && (
                  <>
                    {' '}
                    · <span>{briefingState(row)}</span>
                  </>
                )}
              </small>
              {row.excerpt && <span className="pr-brief-excerpt">{row.excerpt}</span>}
            </span>
            <span aria-hidden="true" className="pr-record-end">
              ›
            </span>
          </a>
        </li>
      ))}
    </ul>
  )
}
function BriefDetail({ row, base, params }: { row: Briefing; base: string; params: URLSearchParams }) {
  return (
    <Panel
      title={row.title || row.business_date + ' 项目简报'}
      action={<a href={withQuery(base + '/reports/brief', params)}>返回简报列表</a>}
    >
      <time dateTime={row.business_date}>{row.business_date}</time>
      {row.body ? (
        <div className="pr-copy pr-brief-body">{row.body}</div>
      ) : (
        <Facts
          rows={[
            ['投递状态', briefingState(row)],
            [
              '可见提交数',
              row.visible_submissions ?? (row.source_state === 'unavailable' ? '来源暂不可核验' : '—'),
            ],
            ['生成时间', row.generated_at],
            ['确认时间', row.delivery_confirmed_at],
            ['目标群', row.target_chat_id],
          ]}
        />
      )}
    </Panel>
  )
}
function ConnectionCheck({
  projectId,
  request,
  signal,
}: {
  projectId: string
  request: ProjectRequest
  signal: AbortSignal
}) {
  const [state, setState] = useState<ReactNode>(null),
    [busy, setBusy] = useState(false)
  async function check() {
    setBusy(true)
    setState('正在验证接入…')
    try {
      const response = z
        .object({
          bot: z.string(),
          group: z.string(),
          account: z.string(),
          action: z.string(),
          public_scope: z.string(),
        })
        .parse(
          await request('/api/v1/projects/' + projectId + '/feishu/check', {
            method: 'POST',
            body: { action: 'project.read', public_scope: 'project_only' },
            signal,
          }),
        )
      if (!signal.aborted)
        setState(
          <Facts
            rows={Object.entries(response).map(([k, value]) => [
              checkFieldLabels[k],
              checkLabels[value] || label(value),
            ])}
          />,
        )
    } catch (error) {
      if (!signal.aborted) setState(failure(error))
    } finally {
      if (!signal.aborted) setBusy(false)
    }
  }
  return (
    <>
      <button type="button" className="button primary" data-check="" disabled={busy} onClick={check}>
        接入验证
      </button>
      <div data-result="" role="status">
        {state}
      </div>
    </>
  )
}

export function ProjectPanels({
  route,
  data: raw,
  people,
  openSettings,
  request,
  signal,
}: {
  route: ProjectRoute
  data: unknown
  people: Map<string, string>
  openSettings: (trigger: HTMLElement) => void
  request: ProjectRequest
  signal: AbortSignal
}) {
  const { id, base, tab, parts } = route
  if (tab === 'overview') {
    const data = overviewSchema.parse(raw)
    const { workspace, milestones } = data
    const project = workspace.project
    const owner = (node: (typeof milestones.items)[number]) =>
      node.owner_name || people.get(node.owner_member_id ?? '') || node.owner_member_id || '—'
    return (
      <>
        <Panel
          title="项目概况"
          action={
            workspace.can_manage_profile && (
              <button
                className="button"
                type="button"
                data-project-settings=""
                aria-haspopup="dialog"
                onClick={(e) => openSettings(e.currentTarget)}
              >
                项目设置
              </button>
            )
          }
        >
          <Facts
            rows={[
              ['项目简称', project.short_code],
              ['负责人', workspace.owner?.display_name],
              ['项目类型', <TypeBadge type={project.project_type} types={workspace.project_types} />],
              ['进展速览', milestones.progress?.record?.title || '待补充'],
              ['计划开始日期', project.planned_start || '—'],
              ['计划结束日期', project.planned_end || '—'],
              ['业务时区', project.business_timezone || '—'],
              ['项目背景', project.background || '—'],
            ]}
          />
        </Panel>
        <Panel title="最近一次项目简报" action={<a href={base + '/reports/brief'}>查看全部简报</a>}>
          <BriefList
            base={base}
            params={new URLSearchParams()}
            items={data.latest_briefing ? [data.latest_briefing] : []}
          />
        </Panel>
        <Panel title="关键节点">
          <Table
            headers={['期限', '节点', '负责人', '状态']}
            items={milestones.items.filter((n) => n.kind === 'milestone')}
            empty="暂无关键节点。"
            cells={(n) => [
              n.occurs_on || '—',
              n.task_id ? (
                <>
                  <a href={base + '/tasks/' + n.task_id}>{n.title}</a>
                  {n.task?.due_at && (
                    <>
                      {' '}
                      · <Deadline row={n.task} />
                    </>
                  )}
                </>
              ) : (
                n.title
              ),
              owner(n),
              taskStages[n.status ?? ''] || label(n.status),
            ]}
          />
          <a href={base + '/tasks'}>查看全部任务</a>
        </Panel>
        <Panel title="当前关注">
          <Table
            headers={['关注事项', '负责人']}
            items={milestones.items.filter((n) => n.kind === 'concern')}
            empty="当前没有记录的关注事项。"
            cells={(n) => [n.title, owner(n)]}
          />
        </Panel>
      </>
    )
  }
  if (tab === 'feishu') {
    const data = connectionSchema.parse(raw)
    if (data.state === 'source_denied')
      return (
        <p className="empty" role="status">
          已失去目标群的访问权限。
        </p>
      )
    return (
      <>
        <Panel title="项目接入">
          <Facts
            rows={[
              ['目标群', data.target_name || data.plan?.target_chat_id || '待绑定'],
              ['接入状态', label(data.state)],
              [
                '每日简报',
                data.plan?.plan_enabled ? [data.plan.local_time, data.plan.timezone].join(' · ') : '已暂停',
              ],
            ]}
          />
          {data.can_check && <ConnectionCheck projectId={id} request={request} signal={signal} />}
        </Panel>
        <Panel title="最近回执">
          <Briefings base={base} items={data.briefings?.items ?? []} />
        </Panel>
      </>
    )
  }
  if (tab === 'reports') {
    const nav = (
      <nav className="pr-segment" aria-label="日报视图">
        {['成员日报', '项目简报'].map((name, i) => (
          <a
            key={name}
            href={base + '/reports' + (i ? '/brief' : '')}
            aria-current={Boolean(i) === Boolean(parts.length) ? 'page' : undefined}
          >
            {name}
          </a>
        ))}
      </nav>
    )
    if (parts.length) {
      const data = z.object({ items: z.array(briefingSchema), total: z.number() }).parse(raw)
      const row = data.items.find((r) => r.id === parts[1])
      return (
        <>
          {nav}
          {row ? (
            <BriefDetail row={row} base={base} params={route.params} />
          ) : (
            <Panel title="项目简报">
              <BriefList base={base} items={data.items} params={route.params} />
              <Pager route={route} total={data.total} />
            </Panel>
          )}
        </>
      )
    }
    const data = z.object({ items: z.array(reportSchema), total: z.number() }).parse(raw)
    return (
      <>
        {nav}
        <Panel title="成员日报">
          <Search route={route} />
          <Table
            headers={['成员', '工作日', '日报', '提交状态']}
            items={data.items}
            empty="暂无成员日报。"
            cells={(r) => [
              r.person_name,
              r.work_date,
              r.note,
              r.submission_state === 'pending' ? '待提交' : label(r.submission_state),
            ]}
          />
          <Pager route={route} total={data.total} />
        </Panel>
      </>
    )
  }
  if (tab === 'tasks') {
    const data = z.object({ items: z.array(sourceTaskSchema) }).parse(raw)
    return (
      <Panel title="关联任务">
        <Table
          headers={['任务', '状态', '优先级', '负责人', '里程碑', '期限']}
          items={data.items}
          empty="暂无任务。"
          cells={(r) => [
            <a href={base + '/tasks/' + r.id}>{r.title}</a>,
            taskStages[r.status] || label(r.status),
            r.priority_label || '未设置',
            r.owner_member_id || '未分配',
            r.milestone_id || '未关联',
            <Deadline row={r} />,
          ]}
        />
      </Panel>
    )
  }
  const data = z.object({ items: z.array(meetingSchema), total: z.number() }).parse(raw)
  return (
    <Panel title={projectTabs[tab]}>
      <Search route={route} />
      <Table
        headers={['标题', '来源信息', '关联任务']}
        items={data.items}
        empty={
          tab === 'meetings'
            ? route.params.get('q')
              ? '没有匹配的会议纪要。'
              : '暂无可读会议纪要。这里收录飞书妙记和标题含“纪要”“会议记录”等关键词的项目文档。'
            : '暂无记录。'
        }
        cells={(r) => {
          const kind = r.kind,
            source = r.resource_token
          return [
            <>
              {r.title || '未命名'}
              {r.file_id && r.version_id && (
                <>
                  {' '}
                  <a
                    download
                    href={
                      '/api/v1/feishu/files/' +
                      encodeURIComponent(r.file_id) +
                      '/versions/' +
                      encodeURIComponent(r.version_id) +
                      '/content?download=1&source_project=' +
                      encodeURIComponent(id)
                    }
                  >
                    下载
                  </a>
                </>
              )}
            </>,
            identifier.test(r.chat_id || '') && identifier.test(r.message_id || '') ? (
              <a href={'#/feishu/bot-chats/' + r.chat_id + '/' + r.message_id}>查看分享消息</a>
            ) : (
              r.created_at || '—'
            ),
            kind && sourceKinds.includes(kind) && identifier.test(source || '') ? (
              <a href={base + '/tasks/source/' + kind + '/' + source}>查看关联任务</a>
            ) : (
              '—'
            ),
          ]
        }}
      />
      <Pager route={route} total={data.total} />
    </Panel>
  )
}
