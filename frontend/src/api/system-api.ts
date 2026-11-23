import { z } from './validation.ts'
import type { RequestOptions } from './session-client.ts'

type Request = (path: string, options?: RequestOptions) => Promise<unknown>
export type SystemQuery = Record<string, string | number>
const root = '/api/v1/system'
const directory = root + '/directory'
const accounts = root + '/accounts'
const grants = root + '/role-grants'
const runs = directory + '/sync-runs'
const bot = root + '/feishu-bot'
const count = z.number().int().nonnegative()
const text = z.string()
const flag = z.boolean()
const scope = z.object({ department_roots: count, explicit_people: count, entire_org: flag })
const nullableText = text.nullable()
const account = z.object({
  id: text,
  name: text,
  personKey: text,
  active: flag,
  avatarUrl: text,
  createdAt: text,
  updatedAt: text,
  systemRoles: text.array(),
  directoryStatus: text.optional(),
  resignedAt: text.optional(),
  disableProtected: flag.optional(),
})
const user = z.object({
  id: text,
  name: text,
  personKey: text,
  source: text,
  org: text,
  status: z.enum(['active', 'disabled', 'unprovisioned']),
  directoryStatus: nullableText,
  directoryUnactivated: flag,
  resignedAt: nullableText,
  disableProtected: flag,
  roleNames: text.array(),
})
const department = z.object({ id: text, parent_id: text, name: text, has_children: flag })
const role = z.object({
  id: text,
  name: text,
  scope: z.enum(['system', 'project']),
  default: flag,
  grantable: flag,
  permissions: text.array(),
})
const grant = z.object({ id: text, accountId: text, role: text, projectId: text.nullish() })
const project = z.object({ id: text, name: text, generation: text, grant: nullableText })
const syncRun = z
  .object({
    id: count,
    state: text,
    created_at: text,
    started_at: nullableText,
    finished_at: nullableText,
    scope: scope.nullable(),
    department_count: count,
    people_count: count,
    new_people_count: count,
    changed_people_count: count,
    missing_people_count: count,
    outside_scope_count: count,
    failure_reason: text.optional(),
    coalesced: flag.optional(),
  })
  .transform((run) => ({
    id: run.id,
    status: run.state,
    createdAt: run.created_at,
    startedAt: run.started_at,
    finishedAt: run.finished_at,
    scope: run.scope,
    departments: run.department_count,
    users: run.people_count,
    addedUsers: run.new_people_count,
    changedUsers: run.changed_people_count,
    missingUsers: run.missing_people_count,
    outsideScopeUsers: run.outside_scope_count,
    failureReason: run.failure_reason,
    coalesced: run.coalesced === true,
  }))
const pageBoundary = z
  .object({ snapshot_version: count.positive().nullable(), visible: flag, total: count })
  .refine((page) => page.visible === (page.snapshot_version !== null), '目录分页响应无效。')
const sync = z.object({
  snapshot_version: count.nullable(),
  visible: flag,
  scope: scope.nullable(),
  last_success_at: nullableText,
  next_run_at: nullableText,
  current: syncRun.nullable(),
  latest_attempt: syncRun.nullable(),
  items: syncRun.array(),
})
const audit = z.object({
  recordId: text,
  createdAt: text,
  actorLabel: text,
  directorySyncRunId: count.optional(),
  kind: text,
  actionLabel: text,
  resourceId: text,
  result: text,
  correlationId: text,
})
const errorCode = z.object({ status: z.number(), data: z.object({ error: text }) })
const botConfig = z.object({
  app_id: text,
  tenant_key: text,
  credentials_configured: flag,
  bot_name: text,
  oauth_callback_url: text,
  enabled: flag,
  version: count,
  connection: z.object({ state: text, last_heartbeat_at: nullableText, last_message_at: nullableText }),
})
const configResponse = z.object({ data: botConfig, etag: nullableText })
export type BotConfig = z.infer<typeof botConfig>
export const checkResultSchema = z.object({
  name: text,
  state: text,
  error: text.optional(),
  checked_at: text.optional(),
})
export const botCheckSchema = z.object({
  id: text,
  state: text,
  results: checkResultSchema.array().optional(),
  reused: flag.optional(),
})

export function feishuBotAPI(request: Request) {
  const checks = bot + '/checks'
  return {
    status: async (signal?: AbortSignal) =>
      configResponse.parse(await request(bot, { includeETag: true, signal })),
    save: async (enabled: boolean, ifMatch: string, signal?: AbortSignal) =>
      configResponse.parse(
        await request(bot, { method: 'PATCH', body: { enabled }, ifMatch, includeETag: true, signal }),
      ),
    test: async (idempotencyKey: string, signal?: AbortSignal) =>
      botCheckSchema.parse(await request(checks, { method: 'POST', body: {}, idempotencyKey, signal })),
    check: async (id: string, signal?: AbortSignal) =>
      botCheckSchema.parse(
        await request(checks + '/' + encodeURIComponent(id), { signal, activity: 'poll' }),
      ),
  }
}
function params(query: SystemQuery) {
  return new URLSearchParams(Object.entries(query).map(([key, value]) => [key, String(value)]))
}
export async function listAudit(request: Request, query: SystemQuery, signal?: AbortSignal) {
  const page = z
    .object({ items: audit.array(), total: count, nextCursor: text.optional() })
    .parse(
      await request(root + '/audit-events?' + params({ sort: 'time', direction: 'desc', ...query }), {
        signal,
      }),
    )
  return {
    ...page,
    items: page.items.map((row) => ({
      id: row.recordId,
      time: row.createdAt,
      actor: row.actorLabel,
      directorySyncRunId: row.directorySyncRunId,
      kind: row.kind,
      action: row.actionLabel,
      target: row.resourceId,
      result: row.result,
      before: '',
      after: '',
      source: row.correlationId,
    })),
  }
}
export function isDirectoryConflict(error: unknown) {
  const result = errorCode.safeParse(error)
  return (
    result.success && result.data.status === 409 && result.data.data.error === 'directory_snapshot_changed'
  )
}
const writeErrors = new Map([
  ['membership_changed', '项目会籍已变更，请重新打开用户详情后选择项目。'],
  ['permission denied', '当前账号没有执行此操作的权限。'],
  ['resource not found', '记录不可用，请刷新后重试。'],
  ['at least one active system administrator is required', '系统必须保留至少一位已启用的管理员。'],
  ['account_resigned', '飞书目录仍显示已离职。人员重新同步为在册后可启用账号。'],
  ['account already exists', '该用户的账号已开通，请刷新列表。'],
  ['role grant already exists', '该用户已有相同范围的角色授权。'],
  ['directory_unavailable', '飞书目录暂不可用，请稍后重试。'],
])
export function systemAPI(request: Request) {
  async function write(path: string, options: RequestOptions) {
    try {
      return await request(path, options)
    } catch (error) {
      const parsed = errorCode.safeParse(error)
      const message = parsed.success && writeErrors.get(parsed.data.data.error)
      if (!message) throw error
      throw Object.assign(new Error(message, { cause: error }), { status: parsed.data.status })
    }
  }
  async function page<S extends z.ZodType>(
    path: string,
    schema: S,
    query: SystemQuery,
    signal?: AbortSignal,
  ) {
    const raw = await request(path + '?' + params(query), { signal })
    const boundary = pageBoundary.parse(raw)
    if (
      query.snapshot_version !== undefined &&
      Number(query.snapshot_version) !== (boundary.snapshot_version ?? 0)
    )
      throw Object.assign(new Error('组织目录已更新，请重试。'), {
        status: 409,
        data: { error: 'directory_snapshot_changed' },
      })
    return { ...boundary, items: z.object({ items: z.array(schema) }).parse(raw).items }
  }
  const catalog = async (signal?: AbortSignal) =>
    z.object({ items: role.array() }).parse(await request(root + '/roles', { signal })).items
  function accountPath(id: string) {
    if (!id || id.startsWith('directory:')) throw new Error('该目录人员尚未开通账号。')
    return accounts + '/' + encodeURIComponent(id)
  }
  return {
    catalog,
    async roles(signal?: AbortSignal) {
      const [roles, counts] = await Promise.all([
        catalog(signal),
        request(root + '/role-counts', { signal }).then((v) => z.record(text, count.nullable()).parse(v)),
      ])
      return roles.map((r) => ({ ...r, count: counts[r.id] ?? (r.scope === 'system' ? 0 : null) }))
    },
    users: (query: SystemQuery, signal?: AbortSignal) => page(root + '/user-page', user, query, signal),
    async children(query: SystemQuery, signal?: AbortSignal) {
      const items: { id: string; parentId: string; name: string; hasChildren: boolean }[] = []
      while (true) {
        const result = await page(
          directory + '/departments',
          department,
          { ...query, limit: 200, offset: items.length },
          signal,
        )
        items.push(
          ...result.items.map((d) => ({
            id: d.id,
            parentId: d.parent_id,
            name: d.name,
            hasChildren: d.has_children,
          })),
        )
        if (items.length >= result.total) return items
        if (!result.items.length) throw new Error('组织节点分页响应无效。')
      }
    },
    async sync(signal?: AbortSignal) {
      const result = sync.parse(await request(runs + '?limit=2', { signal, activity: 'poll' }))
      return {
        snapshotVersion: result.snapshot_version,
        visible: result.visible,
        scope: result.scope,
        lastSuccessAt: result.last_success_at,
        nextRunAt: result.next_run_at,
        run: result.current ?? result.latest_attempt,
        lastSuccess: result.visible
          ? (result.items.find((r) => r.id === result.snapshot_version) ?? null)
          : null,
      }
    },
    syncSummary: async (version: number, signal?: AbortSignal) =>
      syncRun.parse(await request(runs + '/' + version, { signal })),
    startSync: async (signal?: AbortSignal) => syncRun.parse(await write(runs, { method: 'POST', signal })),
    account: async (id: string, signal?: AbortSignal) =>
      account.parse(await request(accountPath(id), { signal })),
    async grants(id: string, signal?: AbortSignal) {
      const items: { id: string; role: string; scope: 'project' | 'system'; project: string }[] = []
      const query = new URLSearchParams({ accountId: id, limit: '100' })
      const seen = new Set<string>()
      while (true) {
        const page = z
          .object({ items: grant.array(), nextCursor: text.optional() })
          .parse(await request(grants + '?' + query, { signal }))
        for (const g of page.items)
          if (g.accountId === id)
            items.push({
              id: g.id,
              role: g.role,
              scope: g.projectId ? 'project' : 'system',
              project: g.projectId ?? '',
            })
        if (!page.nextCursor) return items
        if (seen.has(page.nextCursor)) throw new Error('无法读取完整角色列表。')
        seen.add(page.nextCursor)
        query.set('cursor', page.nextCursor)
      }
    },
    projects: async (id: string, signal?: AbortSignal) =>
      z
        .object({ items: project.array(), canDelegate: flag })
        .parse(await request(accountPath(id) + '/projects', { signal })),
    async provision(id: string, signal?: AbortSignal) {
      const openId = id.startsWith('directory:') && id.slice('directory:'.length)
      if (!openId) throw new Error('请选择尚未开通账号的目录人员。')
      return z.object({ id: text }).parse(await write(accounts, { method: 'POST', signal, body: { openId } }))
    },
    async setAccountStatus(id: string, status: 'active' | 'disabled', signal?: AbortSignal) {
      await write(accountPath(id), { method: 'PATCH', signal, body: { active: status === 'active' } })
    },
    async grant(
      id: string,
      input: { role: z.infer<typeof role>; project?: string; generation?: string },
      signal?: AbortSignal,
    ) {
      const selected = input.role
      if (!selected.grantable || selected.default) throw new Error('该角色无法手动授予。')
      await write(grants, {
        method: 'POST',
        signal,
        body: {
          accountId: id,
          role: selected.id,
          ...(selected.scope === 'project' && {
            projectId: input.project,
            ...(input.generation !== undefined && { generation: input.generation }),
          }),
        },
      })
    },
    async revoke(id: string, signal?: AbortSignal) {
      await write(grants + '/' + encodeURIComponent(id), { method: 'DELETE', signal })
    },
  }
}
export type SystemAPI = ReturnType<typeof systemAPI>
