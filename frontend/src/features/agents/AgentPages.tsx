import { useEffect, useMemo, useRef, useState } from 'react'
import { client } from '../../api/session'
import { errorStatus } from '../../api/session-client'
import { z } from '../../api/validation'
import { Button } from '../../ui/components/Elements'
import type { ProjectRequest } from '../projects/project-contracts'
import { briefReceipts, BriefReceiptLinks } from '../projects/BriefReceiptLinks'
import './agents.css'

const configSchema = z.object({
  name: z.string(),
  role: z.string(),
  persona: z.string(),
  tools: z.array(z.string()).nullable(),
  enabled: z.boolean(),
  version: z.number().int(),
  applied_version: z.number().int(),
  failed_version: z.number().int(),
})
const responseSchema = z.object({
  config: configSchema,
  can_edit: z.boolean(),
  available_tools: z.array(z.string()),
  last_reason: z.string().optional(),
  last_actor: z.string().optional(),
})
type Config = z.infer<typeof configSchema>
type Settings = z.infer<typeof responseSchema>

function configState(c: Config) {
  if (!c.enabled) return '已停用'
  if (!c.version) return '默认配置'
  if (c.version === c.applied_version) return '已生效'
  if (c.failed_version === c.version) return '应用失败'
  return '待应用'
}

function EditConfig({
  data,
  url,
  request,
  close,
  updated,
}: {
  data: Settings
  url: string
  request: ProjectRequest
  close: () => void
  updated: (next: Settings) => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [form, setForm] = useState(data.config),
    [reason, setReason] = useState(''),
    [error, setError] = useState(''),
    [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const active = useRef<AbortController | null>(null)
  useEffect(() => {
    dialog.current?.showModal()
    return () => active.current?.abort()
  }, [])
  async function save() {
    if (active.current || conflict) return
    const controller = new AbortController()
    active.current = controller
    setBusy(true)
    setError('')
    try {
      const next = responseSchema.parse(
        await request(url, {
          method: 'PUT',
          signal: controller.signal,
          body: {
            name: form.name.trim(),
            persona: form.persona.trim(),
            tools: form.tools,
            enabled: form.enabled,
            reason: reason.trim(),
            expected_version: form.version,
          },
        }),
      )
      if (!controller.signal.aborted) {
        updated(next)
        close()
      }
    } catch (e) {
      if (!controller.signal.aborted) {
        const stale = errorStatus(e) === 409
        setConflict(stale)
        setError(
          stale
            ? '已有更新。关闭后重新读取配置，再提交修改。'
            : errorStatus(e) === 403
              ? '当前账号无权保存配置。'
              : '保存失败，请重新读取配置。',
        )
      }
    } finally {
      active.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  return (
    <dialog
      ref={dialog}
      className="agent-editor"
      aria-labelledby="agent-edit-title"
      onCancel={(e) => {
        e.preventDefault()
        if (!busy) close()
      }}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <h2 id="agent-edit-title">编辑 Agent 配置</h2>
        <label>
          Agent 名称
          <input
            value={form.name}
            required
            maxLength={48}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
        </label>
        <label>
          人设与职责
          <textarea
            rows={4}
            maxLength={4000}
            value={form.persona}
            onChange={(e) => setForm({ ...form, persona: e.target.value })}
            placeholder="留空使用角色默认人设"
          />
        </label>
        <fieldset>
          <legend>允许使用的工具</legend>
          <label>
            <input
              type="checkbox"
              checked={form.tools === null}
              onChange={(e) =>
                setForm({ ...form, tools: e.target.checked ? null : [...data.available_tools] })
              }
            />
            使用角色默认工具
          </label>
          {form.tools !== null && (
            <div className="agent-tools">
              {data.available_tools.map((tool) => (
                <label key={tool}>
                  <input
                    type="checkbox"
                    checked={form.tools?.includes(tool) ?? false}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        tools: e.target.checked
                          ? [...(form.tools ?? []), tool]
                          : (form.tools?.filter((t) => t !== tool) ?? []),
                      })
                    }
                  />
                  {tool}
                </label>
              ))}
            </div>
          )}
        </fieldset>
        <label>
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
          />
          启用 Agent
        </label>
        <label>
          变更原因
          <input required maxLength={180} value={reason} onChange={(e) => setReason(e.target.value)} />
        </label>
        {error && <p role="alert">{error}</p>}
        <div className="agent-actions">
          <Button disabled={busy} onClick={close}>
            关闭
          </Button>
          <button
            className="button primary"
            type="submit"
            disabled={busy || conflict || !form.name.trim() || !reason.trim()}
          >
            保存配置
          </button>
        </div>
      </form>
    </dialog>
  )
}

export function AgentConfig({
  url,
  title,
  request,
}: {
  url: string
  title: string
  request: ProjectRequest
}) {
  const [data, setData] = useState<Settings>(),
    [error, setError] = useState(''),
    [revision, reload] = useState(0),
    [edit, setEdit] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    let timer: number | undefined
    async function refresh() {
      try {
        const next = responseSchema.parse(await request(url, { signal: controller.signal, activity: 'poll' }))
        if (controller.signal.aborted) return
        setData(next)
        setError('')
        if (configState(next.config) === '待应用') timer = window.setTimeout(refresh, 5000)
      } catch (e) {
        if (!controller.signal.aborted) {
          setData(undefined)
          setEdit(false)
          setError(errorStatus(e) === 403 ? '无权访问该配置。' : '配置暂时不可用。')
        }
      }
    }
    void refresh()
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [url, revision, request])
  return (
    <section className="panel agent-settings">
      <header className="section-head">
        <h2>{title}</h2>
        {data?.can_edit && <Button onClick={() => setEdit(true)}>编辑配置</Button>}
      </header>
      {error ? (
        <p role="alert">
          {error} <Button onClick={() => reload((i) => i + 1)}>重试</Button>
        </p>
      ) : !data ? (
        <p role="status">正在读取配置…</p>
      ) : (
        <>
          <h3>{data.config.name}</h3>
          <dl className="agent-facts">
            <dt>配置状态</dt>
            <dd>{configState(data.config)}</dd>
            <dt>配置版本</dt>
            <dd>
              {data.config.version} · Runtime 生效版本 {data.config.applied_version}
            </dd>
            <dt>人设与职责</dt>
            <dd>{data.config.persona || '角色默认人设'}</dd>
            {data.last_reason && (
              <>
                <dt>最近变更</dt>
                <dd>
                  {data.last_reason} · {data.last_actor}
                </dd>
              </>
            )}
            <dt>允许使用的工具</dt>
            <dd>
              {data.config.tools?.join('、') ?? '角色默认工具'}
              {data.config.tools?.length === 0 ? '无可选工具' : ''}
            </dd>
          </dl>
          {edit && (
            <EditConfig
              data={data}
              url={url}
              request={request}
              close={() => {
                setEdit(false)
                reload((i) => i + 1)
              }}
              updated={setData}
            />
          )}
        </>
      )}
    </section>
  )
}

export function AgentManagement() {
  const request = useMemo(() => client.bind(), [])
  return (
    <main id="page" className="page">
      <header className="head">
        <h1>Agent 管理</h1>
      </header>
      <AgentConfig
        request={request}
        title="全局管理 Agent"
        url="/api/v1/agents/configurations/management-pm"
      />
      <AgentConfig request={request} title="知识问答 Agent" url="/api/v1/agents/configurations/query" />
    </main>
  )
}

const runsSchema = z.object({
  items: z.array(
    z.object({
      id: z.string(),
      project: z.string(),
      state: z.string(),
      mode: z.string(),
      writes: z.number().int(),
      write_requested: z.boolean(),
      briefs: briefReceipts,
      delivery_id: z.string(),
      platform_id: z.string(),
      intent: z.enum(['project', 'admin', 'knowledge']),
      parent_work: z.string(),
      channel: z.string(),
      source_id: z.string(),
      turn: z.number().int(),
      reason: z.string(),
      task_codes: z.array(z.string()),
      write_failures: z.array(
        z.object({
          slot: z.number().int().min(1).max(128),
          tool: z.string(),
          status: z.union([z.literal(400), z.literal(403), z.literal(409)]),
        }),
      ),
    }),
  ),
})
const stateNames: Record<string, string> = {
  route: '待分派',
  awaiting_project: '待选项目',
  running: '执行中',
  ready: '待交付',
  sending: '投递中',
  sent: '回复已送达',
  unknown: '回执未知',
  suppressed: '已抑制',
  expired: '已过期',
  failed: '失败',
}
const roleNames = { project: '项目 PM', admin: '全局管理', knowledge: '知识问答' }
export function AgentRuns() {
  const [search, setSearch] = useState(''),
    [id, setID] = useState(''),
    [revision, refresh] = useState(0),
    [items, setItems] = useState<z.infer<typeof runsSchema>['items']>(),
    [error, setError] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    setItems(undefined)
    setError('')
    client
      .bind()('/api/v1/agent-runs?work_id=' + encodeURIComponent(id), { signal: controller.signal })
      .then((raw) => {
        if (!controller.signal.aborted) setItems(runsSchema.parse(raw).items)
      })
      .catch(() => {
        if (!controller.signal.aborted) setError('暂时无法读取运行记录。')
      })
    return () => controller.abort()
  }, [id, revision])
  return (
    <main id="page" className="page">
      <header className="head">
        <h1>我的运行记录</h1>
      </header>
      <section className="panel">
        <form
          className="agent-actions"
          onSubmit={(e) => {
            e.preventDefault()
            setID(search.trim())
            refresh((i) => i + 1)
          }}
        >
          <label>
            工作编号 <input value={search} onChange={(e) => setSearch(e.target.value)} />
          </label>
          <button className="button" type="submit">
            查询
          </button>
        </form>
        {error ? (
          <p role="alert">{error}</p>
        ) : !items ? (
          <p role="status">正在读取…</p>
        ) : !items.length ? (
          <p className="empty">暂无符合条件的记录。</p>
        ) : (
          <ul className="agent-runs">
            {items.map((item) => (
              <li key={item.id}>
                <strong>
                  {item.project || '全局'} · {roleNames[item.intent]} · {stateNames[item.state] ?? item.state}
                </strong>
                <dl className="agent-facts">
                  <dt>工作编号</dt>
                  <dd>{item.id}</dd>
                  {item.parent_work && (
                    <>
                      <dt>上级工作</dt>
                      <dd>{item.parent_work}</dd>
                    </>
                  )}
                  <dt>来源</dt>
                  <dd>
                    {item.channel} · {item.source_id}
                  </dd>
                  <dt>模型轮次</dt>
                  <dd>{item.turn || '尚未绑定'}</dd>
                  <dt>已提交的业务写入</dt>
                  <dd>
                    {item.writes === 0 && item.write_requested ? '尚无业务写入回执' : item.writes + ' 条'}
                    {' ' + item.task_codes.join('、')}
                    <BriefReceiptLinks project={item.project} receipts={item.briefs} />
                  </dd>
                  {item.write_failures.length > 0 && (
                    <>
                      <dt>业务写入结果</dt>
                      <dd>
                        {item.writes > 0 ? '部分成功' : '写入失败'} · {item.write_failures.length} 条未提交
                      </dd>
                      <dt>未提交的操作</dt>
                      <dd>
                        <ul aria-label="未提交的操作">
                          {item.write_failures.map((f) => (
                            <li key={f.slot}>
                              槽位 {f.slot} · {f.tool} · {f.status}{' '}
                              {{ 400: '请求无效', 403: '无权限', 409: '状态冲突' }[f.status]}
                            </li>
                          ))}
                        </ul>
                      </dd>
                    </>
                  )}
                  {item.reason && (
                    <>
                      <dt>状态原因</dt>
                      <dd>{item.reason}</dd>
                    </>
                  )}
                  <dt>交付编号</dt>
                  <dd>{item.delivery_id}</dd>
                  <dt>飞书回执</dt>
                  <dd>
                    {item.platform_id || (item.mode === 'project_chat' ? '站内交付' : '无已确认平台消息号')}
                  </dd>
                </dl>
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  )
}
