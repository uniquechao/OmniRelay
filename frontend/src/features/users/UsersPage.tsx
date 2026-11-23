import { useLayoutEffect, useRef, useState, type RefObject } from 'react'
import { client } from '../../api/session'
import type { Guard, User } from '../workbench/Account'
import { bindUsers, scopes } from './admin'
import { DataList, Drawer, type Column } from '../../ui/components/Widgets'
import { Loading } from '../../ui/components/Elements'
import { systemAPI } from '../../api/system-api.ts'

export function UsersPage({
  user,
  guard,
  section = 'users',
}: {
  user: User
  guard: RefObject<Guard>
  section?: 'users' | 'roles'
}) {
  if (section === 'roles') return <RolesPage />
  return <DirectoryPage user={user} guard={guard} />
}
function DirectoryPage({ user, guard }: { user: User; guard: RefObject<Guard> }) {
  const root = useRef<HTMLElement>(null)
  const sessionUser = useRef(user)
  useLayoutEffect(() => {
    const page = root.current!
    const lifetime = new AbortController()
    const signal = lifetime.signal
    void bindUsers(page, systemAPI(client.bind()), { user: sessionUser.current, signal }).then((view) => {
      if (signal.aborted) return
      guard.current = view
      page.querySelector<HTMLElement>('h1')?.focus({ preventScroll: true })
    })
    return () => {
      lifetime.abort()
      guard.current = {}
    }
  }, [guard])
  return (
    <main className="page" id="page" data-layout="list" ref={root}>
      <header className="head admin-heading">
        <h1 id="page-heading" tabIndex={-1}>
          用户管理
        </h1>
        <div className="system-heading-actions">
          <button className="button primary" type="button" data-directory-sync="" title="同步飞书组织与用户">
            同步
          </button>
          <button className="button" type="button" data-directory-details="">
            同步详情
          </button>
        </div>
      </header>
      <>
        <div className="list-toolbar-line" data-directory-notice="" hidden>
          <span className="aux" role="status" />
          <button className="button" type="button" data-directory-retry="" hidden>
            重试
          </button>
        </div>
        <div className="directory-layout">
          <details className="directory-org">
            <summary>组织结构</summary>
            <div className="directory-tree" data-directory-tree="" />
          </details>
          <section id="system-list" className="or-loading-placeholder" />
        </div>
      </>
    </main>
  )
}

type Role = Awaited<ReturnType<ReturnType<typeof systemAPI>['roles']>>[number]

const byName = (row: Role) => row.name
const roleSearch = (row: Role) => [row.name, ...row.permissions].join(' ')
const roleFilters = [
  {
    key: 'scope',
    label: '授权范围',
    options: scopes,
    matches: (row: Role, value: string) => row.scope === value,
  },
]
const roleColumns: Column<Role>[] = [
  { key: 'name', label: '角色名称', render: (row) => <strong>{row.name}</strong>, sort: byName },
  {
    key: 'scope',
    label: '范围',
    render: (row) => (
      <span className="chip" data-tone={row.scope === 'system' ? 'progress' : 'important'}>
        {scopes[row.scope]}
      </span>
    ),
    sort: (row) => scopes[row.scope],
  },
  { key: 'permissions', label: '权限摘要', text: (row) => row.permissions[0] ?? '—', secondary: true },
  {
    key: 'grants',
    label: '授权数',
    text: (row) => (row.default ? '默认拥有 · ' + row.count + ' 人' : String(row.count ?? '—')),
    sort: (row) => String(row.count ?? -1),
  },
]
function RolesPage() {
  const [rows, setRows] = useState<Role[] | null>(null),
    [failed, setFailed] = useState(false),
    [revision, setRevision] = useState(0)
  const [selected, setSelected] = useState<Role | null>(null)
  const heading = useRef<HTMLHeadingElement>(null),
    trigger = useRef<HTMLButtonElement>(null)
  useLayoutEffect(() => {
    const ctrl = new AbortController()
    heading.current?.focus({ preventScroll: true })
    setFailed(false)
    systemAPI(client.bind())
      .roles(ctrl.signal)
      .then((next) => {
        if (!ctrl.signal.aborted) setRows(next)
      })
      .catch(() => {
        if (!ctrl.signal.aborted) setFailed(true)
      })
    return () => ctrl.abort()
  }, [revision])
  return (
    <main className="page" id="page" data-layout="list">
      <header className="head admin-heading">
        <h1 ref={heading} id="page-heading" tabIndex={-1}>
          角色权限
        </h1>
      </header>
      <section id="system-list">
        {failed ? (
          <div className="empty">
            <p role="alert">加载失败，请重试。</p>
            <button className="button" onClick={() => setRevision((n) => n + 1)}>
              重试
            </button>
          </div>
        ) : rows ? (
          <DataList
            rows={rows}
            label="角色权限"
            itemLabel="角色"
            rowLabel={byName}
            search={roleSearch}
            columns={roleColumns}
            filters={roleFilters}
            selection={false}
            actions={[
              {
                label: '查看权限',
                run: (row, button) => {
                  trigger.current = button
                  setSelected(row)
                },
              },
            ]}
          />
        ) : (
          <Loading size="compact" label="正在读取角色权限…" />
        )}
      </section>
      {selected && (
        <Drawer
          title="角色权限"
          description={selected.name}
          onClose={() => setSelected(null)}
          restoreFocus={() => trigger.current ?? heading.current}
        >
          <section className="drawer-section">
            <h3>授权范围</h3>
            <dl className="admin-details">
              <div>
                <dt>范围</dt>
                <dd>{scopes[selected.scope]}</dd>
              </div>
              <div>
                <dt>授权方式</dt>
                <dd>
                  {selected.default ? '默认拥有' : selected.grantable ? '手动授权' : '由项目成员关系确定'}
                </dd>
              </div>
            </dl>
          </section>
          <section className="drawer-section">
            <h3>可执行操作</h3>
            <ul className="admin-permissions">
              {selected.permissions.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          </section>
        </Drawer>
      )}
    </main>
  )
}
