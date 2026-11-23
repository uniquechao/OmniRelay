import * as workbenchUI from '../../ui/widgets'
import * as loadingUI from '../../ui/loading'
import { showTime } from '../audit/audit-display'
import { isDirectoryConflict, type SystemAPI, type SystemQuery } from '../../api/system-api'
import type { User } from '../workbench/Account'
import type { DialogOptions } from '../../ui/components/runtime'
import type { PageState } from '../../ui/components/Widgets'
import { requiredElement, closestControl } from '../../ui/dom'
import { errorStatus } from '../../api/session-client'
import type { ListState } from '../../ui/data-list'
export const scopes = { system: '系统级', project: '项目级' }
type Results<K extends keyof SystemAPI> = Awaited<ReturnType<SystemAPI[K]>>
type Listed = Results<'users'>['items'][number]
type Role = Results<'catalog'>[number]
type Grant = Results<'grants'>[number]
type Projects = Results<'projects'>
type AccountDetail = Listed & {
  roles: Grant[]
  systemRoles: string[]
  canDelegate?: boolean
  projects?: Projects['items']
  projectError?: unknown
}
type DisplayGrant = Pick<Grant, 'scope' | 'role'> & Partial<Grant> & { default?: boolean }
type Run = Results<'startSync'>
type Sync = Results<'sync'>
type Facts = [string, unknown, string?][]
type Confirmation = {
  title: string
  description: string
  message: string
  confirm: string
  change: () => Promise<void>
  trigger: HTMLElement
  restoreFocus?: () => HTMLElement | null
  onConfirmed?: () => void
  tone?: DialogOptions['tone']
}

const statuses = { active: '已启用', disabled: '已停用', unprovisioned: '尚未开通' }
const directoryStatuses: Record<string, string> = {
  active: '在职',
  inactive: '未启用',
  resigned: '已离职',
  unknown: '未知',
}
const directoryStatusLabel = (record: Listed) =>
  record.directoryUnactivated ? '未激活' : (directoryStatuses[record.directoryStatus ?? ''] ?? '未知')
function syncFailure(reason: string | undefined, scopeHidden: boolean) {
  if (/paginat|page|分页/i.test(reason ?? '')) return '飞书目录分页不完整，请重试同步。'
  if (/credentials are missing/i.test(reason ?? '')) return '飞书应用凭据未配置，请联系管理员。'
  if (scopeHidden || /authorization scope changed|unsupported groups/i.test(reason ?? '')) {
    return '飞书通讯录授权已变化，请核对应用的可读范围后重试。'
  }
  if (/HTTP 429|request budget|deadline/i.test(reason ?? '')) return '飞书响应超时或请求受限，请稍后重试。'
  if (/transport unavailable|HTTP 5[0-9]{2}|retries exhausted/i.test(reason ?? ''))
    return '飞书服务暂不可用，请稍后重试。'
  if (/invalid|inconsistent|incomplete|orphaned|unexpected|duplicate/i.test(reason ?? '')) {
    return '飞书目录数据未通过校验，请重试同步。'
  }
  return '同步未完成，请检查飞书应用的通讯录授权和连接状态后重试。'
}
const activeRun = (run: Run | null | undefined): run is Run & { status: 'queued' | 'running' } =>
  ['queued', 'running'].includes(run?.status ?? '')

const statusTone = (status: string) => (status === 'active' ? 'success' : 'pending')
const messageOf = (error: unknown, fallback: string) =>
  error instanceof Error && error.message ? error.message : fallback

export async function bindUsers(
  page: HTMLElement,
  api: SystemAPI,
  { user, signal }: { user: User; signal: AbortSignal },
) {
  const { escape, mountList, mountOrganizationTree, personView, mountPersonCards, openDialog, toast } =
    workbenchUI
  const canManageAccounts = user.permissions?.['account.manage'] === true
  const canManageRoles = user.permissions?.['role.manage'] === true
  const maySync = user.permissions?.['directory.sync'] === true
  const heading = requiredElement(page, '#page-heading', HTMLElement)
  const list = requiredElement(page, '#system-list', HTMLElement)
  const closePersonCard = mountPersonCards(list, { signal })
  const inPage = () => !signal?.aborted && list.isConnected
  const focusHeading = () => (inPage() ? heading : null)
  const syncButton = requiredElement(page, '[data-directory-sync]', HTMLButtonElement)
  const syncDetails = requiredElement(page, '[data-directory-details]', HTMLButtonElement)
  const treeHost = requiredElement(page, '[data-directory-tree]', HTMLElement)
  const notice = requiredElement(page, '[data-directory-notice]', HTMLElement)
  const retry = requiredElement(page, '[data-directory-retry]', HTMLButtonElement)
  const dialogs = new Set<HTMLDialogElement>()
  let userDrawer:
    | { dialog: HTMLDialogElement; dirty: () => boolean; focus: () => void; save: () => Promise<boolean> }
    | undefined
  let navigationPrompt: Promise<boolean> | undefined
  {
    requiredElement(page, '.directory-org', HTMLDetailsElement).open =
      !matchMedia('(max-width: 720px)').matches
    syncButton.hidden = !maySync
    syncButton.disabled = true
    syncDetails.disabled = true
  }
  let pollTimer: number | undefined
  signal?.addEventListener(
    'abort',
    () => {
      clearTimeout(pollTimer)
      for (const dialog of dialogs) dialog.close('cancel')
    },
    { once: true },
  )
  function showDialog(options: DialogOptions) {
    const dialog = openDialog({
      ...options,
      onClose(...args) {
        dialogs.delete(dialog)
        options.onClose?.(...args)
      },
    })
    dialogs.add(dialog)
    return dialog
  }
  function confirmNavigation() {
    const draft = userDrawer
    if (navigationPrompt) return navigationPrompt
    if ([...dialogs].some((dialog) => dialog.open && dialog.hasAttribute('aria-busy')))
      return Promise.resolve(false)
    if (!draft?.dialog.open) return Promise.resolve(true)
    if (!draft.dirty()) return Promise.resolve(true)
    if ([...document.querySelectorAll('dialog[open]')].at(-1) !== draft.dialog) return Promise.resolve(false)
    navigationPrompt = new Promise<boolean>((resolve) => {
      showDialog({
        title: '保存修改？',
        variant: 'confirm',
        tone: 'warning',
        body: '<p class="dialog-note">有未保存的修改，直接关闭会丢失本次更改。</p>',
        cancelLabel: '继续编辑',
        discard: '不保存',
        confirm: '保存并关闭',
        async onClose(confirmed, result) {
          try {
            if (result === 'discard' && draft.dialog.open) draft.dialog.close('cancel')
            const proceed = result === 'discard' || (confirmed && draft.dialog.open && (await draft.save()))
            if (!proceed && draft.dialog.open && !draft.dialog.contains(document.activeElement)) draft.focus()
            resolve(proceed)
          } catch (error) {
            if (draft.dialog.open) fieldError(draft.dialog, messageOf(error, '无法保存修改，请重试。'))
            resolve(false)
          } finally {
            navigationPrompt = undefined
          }
        },
      })
    })
    return navigationPrompt
  }

  const rows: Listed[] = []
  const catalog = new Map<string, Role>()
  let selectedId = ''
  const listStates = new Map<string, ListState>()
  let currentUserQuery: (SystemQuery & { q: string; offset: number }) | undefined
  let treeRequest: Promise<Results<'children'>> | undefined
  let detailRequest = 0
  let conflicts = 0
  let sync: Partial<Sync> | null = null
  let detailsTarget: HTMLElement | undefined
  let lastDetailsMarkup = ''
  let loadingSync = false
  let requestingSync = false
  let directoryMeta: (Pick<Sync, 'snapshotVersion' | 'visible'> & Partial<Sync>) | undefined
  let readForbidden = false
  let syncForbidden = false
  let directoryError = false
  let syncError = false
  let syncRevision = 0
  let syncRequest = 0
  let summaryRequest: number | null | undefined
  let directoryRequest = new AbortController()
  let reloadingAfterConflict = false
  let mounted: ReturnType<typeof mountList<Listed>> | undefined
  const refresh = () => mounted?.refresh()
  const findUser = (id: string) => rows.find((record) => String(record.id) === id)
  const roleName = (id: string) => catalog.get(id)?.name ?? id
  const displayedRoles = (record: AccountDetail): DisplayGrant[] => [
    ...(record.systemRoles ?? [])
      .filter((id) => !record.roles.some((grant) => grant.role === id))
      .map((id): DisplayGrant => ({ role: id, scope: 'system', default: catalog.get(id)?.default === true })),
    ...record.roles,
  ]

  const sourceOf = (record: Listed) => record.source || '—'
  const isCurrent = (record: Listed) => user?.id != null && String(user.id) === String(record.id)
  const badge = (tone: string, label: unknown) =>
    '<span class="chip" data-tone="' + tone + '">' + escape(label) + '</span>'
  const action = (key: string, id: string, label: string, danger = false) =>
    '<button class="row-action' +
    (danger ? ' admin-danger' : '') +
    '" type="button" data-admin-action="' +
    key +
    '" data-record="' +
    escape(id) +
    '">' +
    label +
    '</button>'
  const column = (
    key: string,
    label: string,
    render: (record: Listed) => string,
    sort?: ((record: Listed) => string) | null,
    secondary = false,
  ) => ({ key, label, render, sort: sort ?? undefined, secondary })
  const group = (label: string, body: string) =>
    '<section class="drawer-section"><h3>' + escape(label) + '</h3>' + body + '</section>'
  const details = (entries: Facts) =>
    '<dl class="admin-details">' +
    entries
      .map(
        ([label, value, tone]) =>
          '<div><dt>' +
          escape(label) +
          '</dt><dd>' +
          (tone ? badge(tone, value) : escape(value ?? '—')) +
          '</dd></div>',
      )
      .join('') +
    '</dl>'
  const grantScopeLabel = (grant: DisplayGrant, record: AccountDetail) =>
    grant.scope === 'project'
      ? record.projects?.find((project) => project.id === grant.project)?.name || grant.project || '项目'
      : grant.default
        ? '系统级 · 默认拥有'
        : record.status === 'disabled'
          ? '系统级 · 已停用'
          : '系统级'
  const focusAction = (key: string, id: string) =>
    [...list.querySelectorAll<HTMLElement>('[data-admin-action]')].find(
      (button) => button.dataset.adminAction === key && button.dataset.record === String(id),
    ) ?? focusHeading()

  function configuration(): Parameters<typeof mountList<Listed>>[1] {
    return {
      key: 'system-users',
      label: '用户管理',
      selection: false,
      rows,
      itemLabel: '用户或账号',
      rowLabel: (record) => record.name,
      states: listStates,
      signal: signal ? AbortSignal.any([signal, directoryRequest.signal]) : directoryRequest.signal,
      loadPage: loadUserPage,
      filters: [{ key: 'status', label: '账号状态', options: statuses }],
      columns: [
        column(
          'name',
          '用户',
          (record) => {
            const summary = [record.status === 'unprovisioned' ? '' : record.id, sourceOf(record)]
              .filter(Boolean)
              .join(' · ')
            return (
              personView(record) +
              (summary
                ? '<small class="admin-cell-sub" title="' +
                  escape(summary) +
                  '">' +
                  escape(summary) +
                  '</small>'
                : '')
            )
          },
          (record) => record.name,
        ),
        column(
          'org',
          '组织',
          (record) =>
            record.org
              ? '<span class="admin-org-path" title="' +
                escape(record.org) +
                '">' +
                escape(record.org) +
                '</span>'
              : '—',
          (record) => record.org || '',
          true,
        ),
        column('roles', '角色', (record) => escape(record.roleNames.join('、') || '未授权'), null, true),
        column(
          'status',
          '账号状态',
          (record) =>
            badge(statusTone(record.status), statuses[record.status] ?? '未知') +
            (record.directoryStatus
              ? '<small class="admin-cell-sub">目录 · ' + escape(directoryStatusLabel(record)) + '</small>'
              : '') +
            (record.disableProtected ? '<small class="admin-cell-sub">最后一位管理员保护</small>' : ''),
          (record) => record.status,
        ),
        column(
          'actions',
          '操作',
          (record) =>
            '<div class="admin-actions">' +
            action('user', record.id, '查看') +
            (canManageAccounts &&
            !isCurrent(record) &&
            (record.status === 'active' || (record.status === 'disabled' && !record.resignedAt))
              ? action(
                  'toggle',
                  record.id,
                  record.status === 'active' ? '停用' : '启用',
                  record.status === 'active',
                )
              : '') +
            '</div>',
        ),
      ],
    }
  }
  function replaceCatalog(roles: Role[]) {
    catalog.clear()
    for (const role of roles) catalog.set(role.id, role)
  }
  const orgPanel = requiredElement(page, '.directory-org', HTMLDetailsElement)
  function directorySignal() {
    return signal ? AbortSignal.any([signal, directoryRequest.signal]) : directoryRequest.signal
  }
  function treeError(error: unknown) {
    if (isDirectoryConflict(error)) return retrySnapshot()
    if ([401, 403].includes(errorStatus(error))) return denyDirectory()
    directoryError = true
    updateSyncControls()
  }
  async function paintTree() {
    if (!orgPanel?.open || !directoryMeta?.visible || treeHost.childElementCount || treeRequest) return
    const version = directoryMeta.snapshotVersion
    const treeSignal = directorySignal()
    const loadChildren = (parent: string) =>
      api.children({ parent_id: parent, snapshot_version: version ?? 0 }, treeSignal)
    treeRequest = loadChildren('')
    try {
      const roots = await treeRequest
      if (treeSignal.aborted || !inPage()) return
      mountOrganizationTree(treeHost, roots, {
        loadChildren,
        onLoadError: treeError,
        onChange(selection) {
          if (selection.id === selectedId) return
          selectedId = selection.id
          mounted?.resetPage()
        },
      })
    } catch (error) {
      if (!treeSignal.aborted) treeError(error)
    } finally {
      if (!treeSignal.aborted) treeRequest = undefined
    }
  }
  function clearDirectory() {
    ++detailRequest
    directoryRequest.abort()
    directoryRequest = new AbortController()
    treeRequest = undefined
    selectedId = ''
    const state = listStates.get('system-users')
    if (state) state.page = 0
    const restore = treeHost.contains(document.activeElement) || list.contains(document.activeElement)
    for (const dialog of dialogs) {
      if (dialog !== detailsTarget?.closest('dialog') && dialog.open) dialog.close('cancel')
    }
    closePersonCard?.()
    treeHost.replaceChildren()
    list.replaceChildren()
    rows.length = 0
    mounted = undefined
    if (restore) heading.focus()
  }
  function observeDirectory(next: Pick<Sync, 'visible' | 'snapshotVersion'> & Partial<Sync>) {
    const changed = directoryMeta !== undefined && directoryMeta.snapshotVersion !== next.snapshotVersion
    if (changed) {
      ++syncRevision
      summaryRequest = undefined
      clearDirectory()
    }
    directoryMeta = { ...directoryMeta, ...next }
    return changed
  }
  function denyDirectory() {
    readForbidden = true
    clearTimeout(pollTimer)
    directoryMeta = undefined
    sync = null
    clearDirectory()
    detailsTarget?.closest('dialog')?.close('cancel')
    updateSyncControls()
  }
  function retrySnapshot() {
    clearDirectory()
    directoryMeta = undefined
    ++syncRevision
    summaryRequest = undefined
    sync = null
    schedulePoll(0)
    reloadingAfterConflict = ++conflicts <= 2
    directoryError = !reloadingAfterConflict
    updateSyncControls()
    if (reloadingAfterConflict) reloadDirectory()
  }
  async function loadUserPage(state: PageState, pageSignal: AbortSignal) {
    if (readForbidden) throw Object.assign(new Error(), { status: 403 })
    currentUserQuery = undefined
    closePersonCard?.()
    ++detailRequest
    const query: SystemQuery & { q: string; offset: number } = {
      limit: state.pageSize,
      offset: state.page * state.pageSize,
      q: state.query,
      status: state.filters.status || '',
      department_id: selectedId,
      sort: state.sort?.key || '',
      direction: state.sort?.direction || 'asc',
    }
    if (directoryMeta) query.snapshot_version = directoryMeta.snapshotVersion ?? 0
    try {
      const result = await api.users(query, pageSignal)
      if (pageSignal.aborted) throw pageSignal.reason
      if (directoryMeta && directoryMeta.snapshotVersion !== result.snapshot_version) {
        retrySnapshot()
        return { items: [], total: 0 }
      }
      observeDirectory({ visible: result.visible, snapshotVersion: result.snapshot_version })
      conflicts = 0
      reloadingAfterConflict = false
      currentUserQuery = { ...query, snapshot_version: result.snapshot_version ?? 0 }
      rows.splice(0, rows.length, ...result.items)
      void paintTree()
      updateSyncControls()
      return { ...result, options: { status: statuses } }
    } catch (error) {
      if (!pageSignal.aborted) {
        if (isDirectoryConflict(error)) retrySnapshot()
        if ([403, 401].includes(errorStatus(error))) denyDirectory()
      }
      throw error
    }
  }
  async function refreshUsers() {
    if (!inPage() || readForbidden) return
    if (mounted) return mounted.refresh()
    else mounted = mountList(list, configuration())
  }
  async function refreshGrantRow(record: AccountDetail) {
    const listed = findUser(record.id)
    if (!listed || !inPage()) return
    const roleNames = [...new Set(displayedRoles(record).map((grant) => roleName(grant.role)))]
    if (record.projectError && listed.roleNames.includes(roleName('project_admin'))) {
      roleNames.push(roleName('project_admin'))
    }
    listed.roleNames = roleNames
    const query = currentUserQuery
    const search = query?.q.trim().toLowerCase()
    if (
      !query ||
      !search ||
      [listed.name, listed.id, listed.org, listed.source, ...roleNames]
        .join(' ')
        .toLowerCase()
        .includes(search)
    ) {
      const tr = list.querySelector<HTMLTableRowElement>('tr[data-row="' + CSS.escape(record.id) + '"]')
      const column = configuration().columns.findIndex((col) => col.key === 'roles')
      if (tr) tr.cells[column].textContent = roleNames.join('、') || '未授权'
      return
    }
    // 被筛选掉的行只需补读原页末尾的一条记录。
    const boundary = await api.users(
      {
        ...query,
        limit: 1,
        offset: query.offset + rows.length - 1,
      },
      directorySignal(),
    )
    if (currentUserQuery !== query) return
    rows.splice(rows.indexOf(listed), 1)
    rows.push(...boundary.items)
    await mounted?.replacePage(rows, boundary.total)
  }
  function reloadDirectory() {
    void refreshUsers()
  }

  function busyDialog(dialog: HTMLDialogElement, pending: boolean) {
    dialog.toggleAttribute('aria-busy', pending)
    const controls = [
      ...dialog.querySelectorAll<HTMLButtonElement | HTMLSelectElement>(':is(button,select):not([disabled])'),
    ]
    controls.forEach((control) => {
      control.disabled = pending
    })
    const feedback = document.createElement('span')
    feedback.className = 'or-loading-feedback'
    feedback.innerHTML = loadingUI.markup({ size: 'inline' })
    feedback.setAttribute('role', 'status')
    feedback.setAttribute('aria-label', '正在提交更改…')
    requiredElement(dialog, '.dialog-footer', HTMLElement).prepend(feedback)
    return () => {
      feedback.remove()
      dialog.removeAttribute('aria-busy')
      controls.forEach((control) => {
        control.disabled = false
      })
    }
  }
  function blockBusyDismiss(dialog: HTMLDialogElement) {
    function block(event: Event) {
      if (!dialog.hasAttribute('aria-busy')) return
      event.preventDefault()
      event.stopImmediatePropagation()
    }
    dialog.addEventListener('cancel', block, true)
    dialog.addEventListener(
      'click',
      (event) => {
        if (event.target === dialog) block(event)
      },
      true,
    )
  }
  function fieldError(dialog: HTMLDialogElement, message: string, field?: HTMLElement | null) {
    const error = requiredElement(dialog, '.field-error', HTMLElement)
    error.hidden = !message
    error.textContent = message
    if (!message) return
    if (field) {
      field.setAttribute('aria-invalid', 'true')
      field.setAttribute('aria-describedby', error.id)
      field.focus()
    } else {
      error.tabIndex = -1
      error.focus()
    }
  }
  function clearRoleError(dialog: HTMLDialogElement) {
    dialog.querySelectorAll('[aria-invalid="true"]').forEach((field) => {
      field.removeAttribute('aria-invalid')
      field.removeAttribute('aria-describedby')
    })
    fieldError(dialog, '')
  }
  function confirmChange({
    title,
    description,
    message,
    confirm,
    change,
    trigger,
    restoreFocus,
    onConfirmed,
    tone = 'danger',
  }: Confirmation) {
    const dialog = showDialog({
      title,
      description,
      body: '<p class="dialog-note">' + escape(message) + '</p>',
      confirm,
      tone,
      variant: 'confirm',
      restoreFocus: () => (trigger.isConnected ? trigger : (restoreFocus?.() ?? focusHeading())),
      onClose(confirmed) {
        if (confirmed) onConfirmed?.()
      },
      onSubmit() {
        if (dialog.hasAttribute('aria-busy')) return { keepOpen: true }
        fieldError(dialog, '')
        const unlock = busyDialog(dialog, true)
        void (async () => {
          try {
            await change()
            if (dialog.open) dialog.close('confirm')
          } catch (error) {
            if (dialog.open) fieldError(dialog, messageOf(error, '操作失败，请重试。'))
          } finally {
            unlock()
          }
        })()
        return { keepOpen: true }
      },
    })
    blockBusyDismiss(dialog)
  }

  function toggleUser(record: Listed, trigger: HTMLElement) {
    const active = record.status === 'active'
    const status = active ? 'disabled' : 'active'
    confirmChange({
      title: active ? '停用账号' : '启用账号',
      description: record.name,
      message: active ? '停用后，该用户将无法使用工作台和飞书 Bot。' : '启用后，该用户可按已有授权继续访问。',
      confirm: active ? '确认停用' : '确认启用',
      tone: active ? 'danger' : 'default',
      trigger,
      restoreFocus: () => focusAction('toggle', record.id),
      async change() {
        await api.setAccountStatus(record.id, status, signal)
        if (!inPage()) return
        if ((await refreshUsers()) === false) {
          toast('warning', '账号状态已提交，列表读取失败。请点击列表中的重新读取。')
          return
        }
        toast('success', record.name + '的账号' + statuses[status] + '。')
      },
    })
  }

  function options(items: [string, string][], placeholder: string) {
    return (
      '<option value="">' +
      escape(placeholder) +
      '</option>' +
      items.map(([id, label]) => '<option value="' + escape(id) + '">' + escape(label) + '</option>').join('')
    )
  }
  async function openUser(record: Listed) {
    const ticket = ++detailRequest
    const current = () => inPage() && ticket === detailRequest && !readForbidden
    try {
      if (record.status === 'unprovisioned') {
        showUser({ ...record, roles: [], systemRoles: [] })
        return
      }
      const options = { signal: directorySignal() }
      const [account, roles, definitions, projects] = await Promise.all([
        api.account(record.id, options.signal),
        api.grants(record.id, options.signal),
        api.catalog(options.signal),
        api.projects(record.id, options.signal).catch((error) => ({ items: [], canDelegate: false, error })),
      ])
      if (!current()) return
      replaceCatalog(definitions)
      showUser({
        ...record,
        status: account.active ? 'active' : 'disabled',
        resignedAt: account.resignedAt ?? null,
        disableProtected: account.disableProtected ?? false,
        systemRoles: account.systemRoles ?? [],
        ...projectFields(projects, roles),
      })
    } catch (error) {
      if (current()) toast('error', messageOf(error, '用户详情读取失败，请重试。'))
    }
  }
  function projectFields(response: Projects & { error?: unknown }, roles: Grant[]) {
    const projects = response.items
    return {
      projects,
      canDelegate: response.canDelegate,
      projectError: response.error,
      roles: [
        ...roles,
        ...projects.flatMap((p) =>
          p.grant ? [{ id: p.grant, role: 'project_admin', scope: 'project' as const, project: p.id }] : [],
        ),
      ],
    }
  }

  function showUser(record: AccountDetail) {
    const unprovisioned = record.status === 'unprovisioned'
    let replacementDrawer = false
    const canProvision =
      canManageAccounts &&
      unprovisioned &&
      (record.directoryStatus === 'active' || record.directoryUnactivated)
    const provision = canProvision
      ? '<div class="user-grant-tools">' +
        '<button class="button primary" type="button" data-admin-action="provision">标记为普通用户</button></div>'
      : ''
    const content = () =>
      displayedRoles(record).length
        ? '<ul class="admin-grants">' +
          displayedRoles(record)
            .map(
              (grant) =>
                '<li><div class="user-grant-label"><strong>' +
                escape(roleName(grant.role)) +
                '</strong>' +
                '<small class="user-grant-scope" data-scope="' +
                (grant.scope === 'project' ? 'project' : 'system') +
                '">' +
                escape(grantScopeLabel(grant, record)) +
                '</small></div>' +
                ((grant.scope === 'project' ? record.canDelegate : canManageRoles) &&
                grant.id &&
                catalog.get(grant.role)?.grantable === true &&
                (!isCurrent(record) || grant.role !== 'system_admin')
                  ? action('revoke', grant.id, '撤销', true)
                  : '') +
                '</li>',
            )
            .join('') +
          '</ul>'
        : '<p class="aux">' +
          (record.status === 'unprovisioned'
            ? '飞书目录人员尚未开通账号。开通后可在此授予角色。'
            : '当前无角色授权。') +
          '</p>'
    const roleOptions = options(
      [...catalog.values()]
        .filter(
          (role) =>
            role.grantable === true &&
            !role.default &&
            (role.scope === 'project' ? record.canDelegate || canManageRoles : canManageRoles),
        )
        .map((role) => [role.id, role.name + ' · ' + scopes[role.scope]]),
      '选择角色',
    )
    const editor =
      (canManageRoles || record.canDelegate) && record.status === 'active'
        ? '<div class="user-grant-tools">' +
          '<button class="button" type="button" data-admin-action="add-role" aria-expanded="false" aria-controls="user-grant-editor">' +
          '<span aria-hidden="true">＋</span> 添加角色</button></div>' +
          '<div class="user-grant-editor" id="user-grant-editor" hidden>' +
          '<label class="field admin-field"><span class="field-label">角色</span><select class="select" name="role" disabled required>' +
          roleOptions +
          '</select></label><label class="field admin-field" hidden><span class="field-label">适用项目</span>' +
          '<select class="select" name="scope" disabled></select></label>' +
          '<p class="aux full-field" data-project-empty hidden>该用户没有可授权的项目。</p>' +
          '<div class="user-grant-buttons"><button class="button neutral" type="button" data-admin-action="cancel-role">取消</button>' +
          '<button class="button primary" type="submit" data-save-role>保存</button></div></div>'
        : ''
    const dialog = showDialog({
      title: '用户详情',
      description: record.name,
      drawer: 'normal',
      tone: 'info',
      restoreFocus: () => (inPage() && !replacementDrawer ? focusAction('user', record.id) : null),
      body:
        group(
          '账号信息',
          details([
            ['账号编号', record.status === 'unprovisioned' ? '尚未开通' : record.id],
            ['所属组织', record.org || '—'],
            ['身份来源', sourceOf(record)],
            ['账号状态', statuses[record.status] ?? '未知', statusTone(record.status)],
            ...(record.directoryStatus ? ([['目录状态', directoryStatusLabel(record)]] satisfies Facts) : []),
            ...(record.resignedAt
              ? ([['离职确认时间', showTime(record.resignedAt) + ' (UTC+08:00)']] satisfies Facts)
              : []),
            ...(record.disableProtected
              ? ([
                  ['停用保护', '同步尝试停用时，该账号是最后一位已启用的系统管理员。转交权限后可停用。'],
                ] satisfies Facts)
              : []),
          ]) + provision,
        ) +
        group(
          '角色授权',
          (record.projectError
            ? '<p role="alert">项目授权读取失败。<button type="button" class="button" data-admin-action="retry-projects">重新读取</button></p>'
            : !unprovisioned && !canManageRoles && !record.canDelegate
              ? '<p class="aux">当前账号无授权操作权限。</p>'
              : '') +
            '<div data-user-grants>' +
            content() +
            '</div>' +
            editor +
            '<span class="sr-only" data-grant-status role="status"></span>',
        ),
      isDirty: () => dirty(),
      onClose() {
        window.removeEventListener('beforeunload', warnBeforeUnload)
        if (userDrawer?.dialog === dialog) userDrawer = undefined
      },
      onSubmit(_, submitter) {
        if (!row || row.hidden || dialog.hasAttribute('aria-busy')) return { keepOpen: true }
        clearRoleError(dialog)
        const grant = readGrant()
        if ('error' in grant) return grant.error
        void saveGrant(grant, !submitter?.matches('[data-save-role]'))
        return { keepOpen: true }
      },
    })
    dialog.classList.add('user-detail-drawer')
    blockBusyDismiss(dialog)
    const row = dialog.querySelector<HTMLElement>('.user-grant-editor')
    const add = dialog.querySelector<HTMLButtonElement>('[data-admin-action="add-role"]')
    const form = requiredElement(dialog, 'form', HTMLFormElement)
    const grantBody = requiredElement(dialog, '[data-user-grants]', HTMLElement)
    const save = dialog.querySelector<HTMLButtonElement>('[data-save-role]')
    const roleField = row && requiredElement(row, '[name=role]', HTMLSelectElement)
    const scopeField = row && requiredElement(row, '[name=scope]', HTMLSelectElement)
    const emptyProjectsMessage = row && requiredElement(row, '[data-project-empty]', HTMLElement)
    let fetchingGrants = false
    const dirty = () => Boolean(roleField?.value)
    function warnBeforeUnload(event: BeforeUnloadEvent) {
      event.preventDefault()
      event.returnValue = true
    }
    function updateUnloadGuard() {
      if (dirty()) window.addEventListener('beforeunload', warnBeforeUnload)
      else window.removeEventListener('beforeunload', warnBeforeUnload)
    }
    function readGrant() {
      if (!roleField || !scopeField || !emptyProjectsMessage)
        return { error: { field: dialog, message: '当前账号无授权操作权限。' } }
      const role = catalog.get(roleField.value)
      const project = role?.scope === 'project' ? scopeField.value : ''
      const invalid = (field: HTMLElement, message: string) => ({ error: { field, message } })
      if (!role || role.grantable !== true || role.default) return invalid(roleField, '请选择可授权的角色。')
      if (role.scope === 'project' && !project) {
        return scopeField.disabled
          ? invalid(roleField, emptyProjectsMessage.textContent || '暂无可选项目')
          : invalid(scopeField, '请选择项目。')
      }
      if ((record.roles ?? []).some((grant) => grant.role === role.id && (grant.project || '') === project)) {
        return invalid(project ? scopeField : roleField, '该用户已有相同范围的角色授权。')
      }
      return { role, project }
    }
    async function saveGrant({ role, project }: { role: Role; project: string }, closeAfter: boolean) {
      const unlock = busyDialog(dialog, true)
      try {
        await api.grant(
          record.id,
          { role, project, generation: record.projects?.find((p) => p.id === project)?.generation },
          signal,
        )
      } catch (error) {
        unlock()
        if (dialog.open)
          fieldError(dialog, messageOf(error, '角色保存失败，请重试。'), project ? scopeField : roleField)
        return false
      }
      unlock()
      if (!inPage() || !dialog.open) return false
      requiredElement(dialog, '[data-grant-status]', HTMLElement).textContent = '角色已保存。'
      toast('success', '已为' + record.name + '授予' + role.name + '。')
      setEditor(false)
      if (!(await updateGrants(role.scope))) return false
      if (closeAfter && dialog.open) dialog.close('confirm')
      return true
    }
    userDrawer = {
      dialog,
      dirty,
      focus: () => (roleField ?? requiredElement(dialog, '.dialog-close', HTMLElement)).focus(),
      async save() {
        if (!form.reportValidity()) return false
        const grant = readGrant()
        if (!('error' in grant)) return saveGrant(grant, true)
        fieldError(dialog, grant.error.message, grant.error.field)
        return false
      },
    }

    async function updateGrants(scope: string) {
      dialog.querySelector('[data-retry-grants]')?.remove()
      fetchingGrants = true
      if (add) add.disabled = true
      grantBody.querySelectorAll('button').forEach((button) => {
        button.disabled = true
      })
      const indicator = document.createElement('div')
      requiredElement(dialog, '.dialog-footer', HTMLElement).prepend(indicator)
      const loading = loadingUI.mount(indicator, { size: 'inline' })
      indicator.setAttribute('aria-label', '正在刷新角色授权…')
      try {
        if (scope === 'project') {
          const response = await api.projects(record.id, signal)
          Object.assign(
            record,
            projectFields(
              response,
              record.roles.filter((g) => g.scope !== 'project'),
            ),
          )
        } else {
          const [account, grants] = await Promise.all([
            api.account(record.id, signal),
            api.grants(record.id, signal),
          ])
          record.roles = [...grants, ...record.roles.filter((g) => g.scope === 'project')]
          record.systemRoles = account.systemRoles
        }
        await refreshGrantRow(record)
        if (!dialog.open) return false
        grantBody.innerHTML = content()
        return true
      } catch {
        if (dialog.open && inPage()) {
          const retry = document.createElement('button')
          retry.type = 'button'
          retry.className = 'button'
          retry.dataset.retryGrants = ''
          retry.textContent = '变更已提交，重新读取授权'
          retry.onclick = () => void updateGrants(scope)
          grantBody.after(retry)
          retry.focus()
        }
        return false
      } finally {
        loading.finish()
        indicator.remove()
        fetchingGrants = false
        if (add)
          add.disabled =
            (!canManageRoles && !record.canDelegate) || Boolean(dialog.querySelector('[data-retry-grants]'))
        if (dialog.open && !row?.hidden) roleField?.focus()
        else if (dialog.open && add && document.activeElement === document.body) add.focus()
      }
    }
    function syncProjects() {
      if (!scopeField || !roleField || !row || !emptyProjectsMessage || !save) return
      const field = scopeField
      const isProject = catalog.get(roleField.value)?.scope === 'project'
      const previous = field.value
      const available = record.projects ?? []
      requiredElement(row, 'label:has([name=scope])', HTMLLabelElement).hidden = !isProject
      const emptyProjects = Boolean(isProject && !available.length)
      field.required = Boolean(isProject)
      field.disabled = !isProject || emptyProjects
      field.innerHTML = options(
        available.map((item) => [item.id, item.name]),
        emptyProjects ? '暂无可选项目' : '选择项目',
      )
      if (available.some((item) => item.id === previous)) field.value = previous
      emptyProjectsMessage.textContent = record.projectError
        ? '项目候选读取失败。'
        : !record.canDelegate
          ? '当前账号没有项目委派权限。'
          : '该用户没有可授权的项目。'
      emptyProjectsMessage.hidden = !emptyProjects
      save.disabled = emptyProjects
    }
    function setEditor(open: boolean) {
      if (!row || !add || !roleField) return
      row.hidden = !open
      add.hidden = open
      add.setAttribute('aria-expanded', String(open))
      if (!open) form.reset()
      roleField.disabled = !open
      syncProjects()
      updateUnloadGuard()
      clearRoleError(dialog)
      ;(open ? roleField : add).focus()
    }
    if (row && roleField) {
      form.addEventListener('input', () => clearRoleError(dialog))
      roleField.addEventListener('change', () => {
        syncProjects()
        updateUnloadGuard()
      })
      syncProjects()
    }
    function provisionUser(trigger: HTMLElement) {
      let refreshed: Listed | undefined
      confirmChange({
        title: '标记为普通用户',
        description: record.name,
        tone: 'default',
        message: '确认后将启用 OmniRelay 账号。该用户可通过飞书身份验证登录系统。',
        confirm: '确认标记',
        trigger,
        async change() {
          const created = await api.provision(record.id, signal)
          if (!inPage() || !dialog.open) return
          try {
            if ((await refreshUsers()) === false) {
              toast('warning', '账号已开通，列表读取失败。请点击列表中的重新读取。')
              return
            }
            if (created?.id && dialog.open)
              refreshed = findUser(String(created.id)) ?? { ...record, id: created.id, status: 'active' }
          } catch {
            if (inPage()) toast('warning', '账号已开通，用户列表刷新失败。请重新打开用户管理。')
            return
          }
          if (inPage() && dialog.open) toast('success', '已将' + record.name + '标记为普通用户。')
        },
        onConfirmed() {
          if (!inPage() || !dialog.open) return
          replacementDrawer = Boolean(refreshed)
          dialog.close('confirm')
          if (refreshed) void openUser(refreshed)
        },
      })
    }
    dialog.addEventListener('click', (event) => {
      if (fetchingGrants || dialog.hasAttribute('aria-busy')) return
      const button = closestControl(event, '[data-admin-action]')
      if (!button) return
      if (button.dataset.adminAction === 'retry-projects') {
        if (dirty()) {
          fieldError(dialog, '请先保存或取消当前角色草稿。')
          return
        }
        replacementDrawer = true
        dialog.close('cancel')
        void openUser(record)
        return
      }
      if (button.dataset.adminAction === 'provision' && canProvision) provisionUser(button)
      if (button.dataset.adminAction === 'add-role') setEditor(true)
      if (button.dataset.adminAction === 'cancel-role') setEditor(false)
      if (button.dataset.adminAction !== 'revoke') return
      const grant = (record.roles ?? []).find((item) => String(item.id) === button.dataset.record)
      if (!grant) return
      confirmChange({
        title: '撤销授权',
        description: record.name,
        trigger: button,
        restoreFocus: () => add ?? requiredElement(dialog, '.dialog-close', HTMLElement),
        message:
          '撤销后，该账号将失去「' +
          roleName(grant.role) +
          ' · ' +
          grantScopeLabel(grant, record) +
          '」对应的访问权限。',
        confirm: '确认撤销',
        async change() {
          await api.revoke(grant.id, signal)
          if (!inPage()) return
          if (dialog.open && !(await updateGrants(grant.scope))) return
          toast('success', '角色授权已撤销。')
        },
      })
    })
  }

  function scopeLabel(scope: Run['scope'] | undefined) {
    if (!scope) return '—'
    if (scope.entire_org) return '整组织'
    return scope.department_roots + ' 个授权部门 · ' + scope.explicit_people + ' 位单独授权人员'
  }
  const count = (value: number | undefined, unit: string) =>
    Number.isInteger(value) ? value + ' ' + unit : '—'
  function runCounts(run: Run): Facts {
    return [
      ['部门', count(run?.departments, '个')],
      ['人员', count(run?.users, '人')],
      ['新增人员', count(run?.addedUsers, '人')],
      ['变更人员', count(run?.changedUsers, '人')],
      ['目录缺失', count(run?.missingUsers, '人')],
      ['授权范围外', count(run?.outsideScopeUsers, '人')],
    ]
  }
  function updateSyncControls() {
    if (!inPage()) return
    const run = sync?.run
    const active = activeRun(run)
    const waiting = run?.status === 'queued'
    const visible = directoryMeta?.visible
    const lastTime = directoryMeta?.lastSuccessAt
    const visibility = visible
      ? run?.status === 'failed'
        ? '本轮同步失败，仍可浏览上次同步的目录。'
        : active
          ? '同步期间可浏览上次同步的目录。'
          : ''
      : lastTime
        ? '飞书通讯录授权已变化，旧目录已隐藏。'
        : directoryMeta
          ? '尚无可见的飞书目录，首次同步成功后可浏览。'
          : ''
    const error = readForbidden
      ? '当前账号没有查看飞书目录的权限。'
      : directoryError
        ? '无法刷新组织与人员，请重试。'
        : reloadingAfterConflict
          ? '组织目录已更新，正在重新加载…'
          : syncError
            ? '暂时无法获取同步状态，请重试。'
            : ''
    const status = active
      ? waiting
        ? '同步排队中…'
        : '同步进行中…'
      : !visible && run?.status === 'failed'
        ? '本轮同步失败，请查看同步详情。'
        : ''
    const noticeText = [status, error, readForbidden ? '' : visibility].filter(Boolean).join(' ')
    notice.hidden = !noticeText
    const message = requiredElement(notice, ':scope > span', HTMLElement)
    if (message.textContent !== noticeText) message.textContent = noticeText
    const hideRetry = readForbidden || !(directoryError || syncError)
    if (hideRetry && document.activeElement === retry) heading.focus()
    retry.hidden = hideRetry
    syncButton.hidden = !maySync || syncForbidden || readForbidden
    syncButton.disabled = requestingSync || active || !directoryMeta
    loadingUI.button(
      syncButton,
      requestingSync || active,
      requestingSync ? '提交中…' : waiting ? '等待同步…' : active ? '同步中…' : '同步',
    )
    syncDetails.disabled = readForbidden
    if (!detailsTarget) return
    const labels: Record<string, [string, string]> = {
      queued: ['pending', '等待同步'],
      running: ['progress', '进行中'],
      succeeded: ['success', '已完成'],
      failed: ['danger', '同步失败'],
    }
    const [tone, label] = labels[run?.status ?? ''] ?? [
      'pending',
      syncError ? '无法获取同步状态' : loadingSync ? '正在查询…' : '暂无同步记录',
    ]
    const phases: Record<string, string> = {
      running: run?.scope ? '读取飞书组织与人员' : '核对飞书通讯录授权',
      succeeded: '组织与人员已更新',
      failed: '飞书目录未更新',
    }
    const phase = phases[run?.status ?? ''] ?? label
    const statusTime = run?.finishedAt
      ? '完成于 ' + showTime(run.finishedAt)
      : run?.startedAt
        ? '开始于 ' + showTime(run.startedAt)
        : run?.createdAt
          ? '排队于 ' + showTime(run.createdAt)
          : ''
    const failure =
      run?.status === 'failed' ? syncFailure(run.failureReason, !visible && Boolean(lastTime)) : ''
    const last =
      visible && sync && sync.snapshotVersion === directoryMeta?.snapshotVersion ? sync.lastSuccess : null
    const markup =
      '<section class="directory-sync-status"><div><strong>' +
      escape(phase) +
      '</strong>' +
      badge(tone, label) +
      '</div>' +
      (active || run?.status === 'succeeded'
        ? loadingUI.markup({
            size: 'compact',
            label: active ? phase + '…' : phase,
            progress: active ? null : 100,
            active,
          })
        : '') +
      (statusTime ? '<p class="aux">' + escape(statusTime) + '</p>' : '') +
      (failure ? '<p class="aux">失败原因：' + escape(failure) + '</p>' : '') +
      (visibility ? '<p class="aux">' + escape(visibility) + '</p>' : '') +
      '</section>' +
      group(
        '同步安排',
        details([
          ['最近成功', showTime(lastTime)],
          ['下次同步', showTime(directoryMeta?.nextRunAt)],
          ['可见目录授权范围', scopeLabel(directoryMeta?.scope)],
        ]),
      ) +
      (run ? group('本轮同步', details([['本轮授权范围', scopeLabel(run.scope)], ...runCounts(run)])) : '') +
      (last && last.id !== run?.id ? group('上次可见快照', details(runCounts(last))) : '')
    if (markup !== lastDetailsMarkup) {
      detailsTarget.innerHTML = markup
      lastDetailsMarkup = markup
    }
  }
  function schedulePoll(delay = activeRun(sync?.run) ? 1200 : 5000) {
    clearTimeout(pollTimer)
    if (inPage() && !readForbidden) pollTimer = window.setTimeout(readSync, delay)
  }
  async function recheckDirectoryAccess() {
    const checkSignal = directorySignal()
    try {
      const page = await api.users(
        {
          limit: 1,
          offset: 0,
          ...(directoryMeta && { snapshot_version: directoryMeta.snapshotVersion ?? 0 }),
        },
        checkSignal,
      )
      if (!checkSignal.aborted && directoryMeta && page.snapshot_version !== directoryMeta.snapshotVersion)
        retrySnapshot()
    } catch (error) {
      if (checkSignal.aborted) return
      if (isDirectoryConflict(error)) retrySnapshot()
      else if ([401, 403].includes(errorStatus(error))) denyDirectory()
    }
  }
  async function readSync() {
    if (requestingSync || readForbidden || !inPage()) return
    if (loadingSync) {
      schedulePoll(1200)
      return
    }
    clearTimeout(pollTimer)
    const requestID = ++syncRequest
    const revision = syncRevision
    const current = () => inPage() && !readForbidden && requestID === syncRequest
    loadingSync = true
    try {
      const next = await api.sync(signal)
      if (!current() || revision !== syncRevision) return
      const previous = sync?.run
      const changed = observeDirectory(next)
      if (!changed && next.visible && !next.lastSuccess && sync?.snapshotVersion === next.snapshotVersion) {
        next.lastSuccess = sync?.lastSuccess ?? null
      }
      sync = next
      syncError = false
      if (changed) reloadDirectory()
      void paintTree()
      if (activeRun(previous) && next.run?.id === previous.id && !activeRun(next.run)) {
        if (next.run.status === 'succeeded') toast('success', '组织与人员同步完成。')
        if (next.run.status === 'failed') toast('error', '组织与人员同步失败，请查看同步详情。')
      }
      updateSyncControls()
      readPreviousSummary()
    } catch (error) {
      if (!current()) return
      syncError = true
      if ([401, 403].includes(errorStatus(error))) void recheckDirectoryAccess()
    } finally {
      loadingSync = false
      if (current()) {
        updateSyncControls()
        schedulePoll(syncError ? 3500 : undefined)
      }
    }
  }
  function readPreviousSummary() {
    if (
      !detailsTarget ||
      !sync?.visible ||
      sync.lastSuccess ||
      sync.snapshotVersion !== directoryMeta?.snapshotVersion
    )
      return
    const version = sync.snapshotVersion
    if (version == null || summaryRequest === version) return
    summaryRequest = version
    void api
      .syncSummary(version, signal)
      .then((result) => {
        if (
          !inPage() ||
          summaryRequest !== version ||
          directoryMeta?.snapshotVersion !== version ||
          !sync ||
          sync.snapshotVersion !== version
        )
          return
        sync.lastSuccess = result
        updateSyncControls()
      })
      .catch((error) => {
        if (!inPage() || summaryRequest !== version || directoryMeta?.snapshotVersion !== version) return
        summaryRequest = undefined
        syncError = true
        updateSyncControls()
        if ([401, 403].includes(errorStatus(error))) void recheckDirectoryAccess()
      })
  }
  async function startSync() {
    if (readForbidden || syncForbidden || !maySync || requestingSync) return
    ++syncRequest
    clearTimeout(pollTimer)
    requestingSync = true
    updateSyncControls()
    syncDetails.focus()
    try {
      const run = await api.startSync(signal)
      if (!inPage() || readForbidden) return
      sync = { ...sync, run }
      toast(
        run.coalesced ? 'info' : 'success',
        run.coalesced ? '已有同步任务，已并入该任务。' : '已提交同步请求。',
      )
    } catch (error) {
      if (!inPage() || readForbidden || errorStatus(error) === 401) return
      if (errorStatus(error) === 403) syncForbidden = true
      toast(
        'error',
        errorStatus(error) === 403 ? '当前账号无法启动同步，请重新登录后再试。' : '无法启动同步，请重试。',
      )
    } finally {
      requestingSync = false
      updateSyncControls()
      schedulePoll(0)
    }
  }
  function showSyncDetails() {
    const dialog = showDialog({
      title: '同步详情',
      description: '飞书组织与用户',
      drawer: 'normal',
      tone: 'info',
      body: '<div class="directory-sync-detail" data-sync-detail role="status"></div>',
      restoreFocus: () => (inPage() && !syncDetails.disabled ? syncDetails : focusHeading()),
      onClose: () => {
        detailsTarget = undefined
      },
    })
    dialog.classList.add('user-detail-drawer')
    detailsTarget = requiredElement(dialog, '[data-sync-detail]', HTMLElement)
    lastDetailsMarkup = ''
    updateSyncControls()
    readPreviousSummary()
    void readSync()
  }

  async function load() {
    const loading = loadingUI.mount(list, { size: 'compact', label: '正在读取' + '用户管理' + '…' })
    try {
      await refreshUsers()
    } catch (error) {
      if (!inPage() || directoryError) return
      list.innerHTML =
        '<div class="empty"><p role="alert"></p><button class="button" type="button">重试</button></div>'
      requiredElement(list, 'p', HTMLElement).textContent = messageOf(error, '加载失败，请重试。')
      requiredElement(list, 'button', HTMLButtonElement).addEventListener('click', load)
    } finally {
      loading.finish()
      list.classList.remove('or-loading-placeholder')
    }
  }
  list.addEventListener('click', (event) => {
    const trigger = closestControl(event, '[data-admin-action]')
    if (!trigger) return
    const id = trigger.dataset.record ?? ''
    const record = findUser(id)
    if (!record) return
    switch (trigger.dataset.adminAction) {
      case 'user':
        void openUser(record)
        break
      case 'toggle':
        if (canManageAccounts) toggleUser(record, trigger)
        break
    }
  })
  {
    orgPanel.addEventListener('toggle', paintTree)
    syncButton.addEventListener('click', startSync)
    syncDetails.addEventListener('click', showSyncDetails)
    retry.addEventListener('click', () => {
      if (directoryError) {
        directoryError = false
        conflicts = 0
        reloadDirectory()
        void paintTree()
      }
      void readSync()
    })
  }
  await load()
  if (inPage()) {
    updateSyncControls()
    void readSync()
  }
  return { confirmNavigation, hasDraft: () => Boolean(userDrawer?.dirty()) }
}
