import { ApiError } from '../../api/session-client'
import { closestControl, requiredElement } from '../../ui/dom'
import type { Choice } from '../../ui/components/runtime'
import { driveFile, type DriveFile, type DriveRequest, type Refresh } from './drive-contracts'

export type UpdateFile = (row: DriveFile | null) => void
type EditOptions = {
  row: DriveFile
  trigger: HTMLButtonElement
  request: DriveRequest
  signal: AbortSignal
  refresh: Refresh
  updated?: UpdateFile
  restoreFocus: () => HTMLElement | null
}
import * as workbenchUI from '../../ui/widgets'
import { assignmentAPI } from '../chat/chat-assignment'

export const syncStates: Record<string, string> = {
  pending: '等待同步',
  complete: '已同步',
  partial: '部分同步',
  failed: '同步失败',
  expired: '同步版本已过期',
  revoked: '访问受限',
  over_limit: '超过同步大小上限',
}

export const syncStateLabel = (state: string) => syncStates[state] || '同步状态未知'

const sourceTime = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'short',
  timeStyle: 'medium',
  hour12: false,
})

export function fileSources(row: DriveFile, projectId: string | null, { compact = false } = {}) {
  const { escape, fileSourceLocator } = workbenchUI
  const seen = new Set()
  const links = (row.sources ?? []).flatMap((source) => {
    if (
      ![source.chat_id, source.message_id].every(
        (id) => typeof id === 'string' && /^[A-Za-z0-9_-]{1,160}$/.test(id),
      )
    )
      return []
    const base =
      projectId && source.project_id === projectId
        ? '#/projects/' + encodeURIComponent(projectId) + '/chats/'
        : '#/feishu/bot-chats/'
    const route = base + source.chat_id + '/' + source.message_id
    if (seen.has(route)) return []
    seen.add(route)
    const group = source.chat_name || source.chat_id
    if (compact) return fileSourceLocator(row.id, group, route, seen.size)
    const details = [source.sender_name || '未知分享人']
    const sent = Date.parse(source.sent_at)
    if (Number.isFinite(sent)) details.push(sourceTime.format(sent))
    if (source.sync_state && source.sync_state !== row.sync_state)
      details.push(syncStateLabel(source.sync_state))
    return (
      '<a class="drive-source" data-source-file="' +
      escape(row.id) +
      '" href="' +
      route +
      '">' +
      '<span class="drive-source-group">' +
      escape(group) +
      '</span>' +
      '<span class="drive-source-detail">' +
      escape(details.join(' · ')) +
      '</span>' +
      '<span class="drive-source-action">定位原消息</span></a>'
    )
  })
  return links.length ? links.join('') : '—'
}

export function fileProjects(row: DriveFile, names: Map<string, string>) {
  const { escape } = workbenchUI
  const chips = row.project_ids.map(
    (id) =>
      '<span class="kind">' +
      escape(names.get(id) || id) +
      (row.inherited_project_ids?.includes(id) ? ' · 来自群聊' : '') +
      '</span>',
  )
  return (
    '<button class="row-action drive-projects" type="button" data-drive-projects="' +
    escape(row.id) +
    '" aria-label="修改 ' +
    escape(row.name) +
    ' 的所属项目">' +
    (chips.join('') || '添加所属项目') +
    '</button>'
  )
}

export async function editFileProjects({
  row,
  trigger,
  request,
  signal,
  refresh,
  updated,
  restoreFocus,
}: EditOptions) {
  const { combo, mountCombo, openDialog, toast } = workbenchUI
  const path = '/api/v1/feishu/files/' + encodeURIComponent(row.id)
  let snapshot: DriveFile, choices: Choice[]
  trigger.disabled = true
  try {
    ;[snapshot, choices] = await Promise.all([
      request(path, { signal }).then((raw) => driveFile.parse(raw)),
      assignmentAPI(request).choices(signal),
    ])
  } catch (error) {
    if (signal.aborted) return
    if (error instanceof ApiError && [403, 404].includes(error.status)) {
      updated?.(null)
      refresh()
    }
    toast('error', '暂时无法读取文件或项目，请重试。')
    return
  } finally {
    trigger.disabled = false
  }
  if (signal.aborted || !trigger.isConnected) return
  const current = new Set(snapshot.manual_project_ids ?? [])
  const known = new Set(choices.map((item) => item.id))
  for (const id of current) if (!known.has(id)) choices.push({ id, label: id })
  const read = (form: HTMLFormElement) =>
    new Set(requiredElement(form, '[data-combo-result]', HTMLInputElement).value.split(',').filter(Boolean))
  const dirty = (form: HTMLFormElement) => {
    const next = read(form)
    return next.size !== current.size || [...next].some((id) => !current.has(id))
  }
  let busy = false
  const close = () => dialog.close()
  const dialog = openDialog({
    title: '修改所属项目',
    description: snapshot.name,
    variant: 'form',
    confirm: '保存',
    body:
      (snapshot.inherited_project_ids?.length
        ? '<p>来自群聊：' +
          snapshot.inherited_project_ids
            .map((id) => workbenchUI.escape(choices.find((choice) => choice.id === id)?.label || id))
            .join('、') +
          '</p>'
        : '') + combo('drive-project-picker', '人工关联项目', 'projects', true),
    isDirty: dirty,
    async onSubmit(form) {
      if (busy) return { keepOpen: true }
      if (!dirty(form)) return
      const next = read(form)
      const submit = requiredElement(form, '[type="submit"]', HTMLButtonElement)
      busy = true
      for (const control of form.querySelectorAll<HTMLInputElement | HTMLButtonElement | HTMLSelectElement>(
        'input,button,select',
      ))
        control.disabled = true
      try {
        await request(path + '/projects', {
          method: 'PATCH',
          signal,
          body: {
            project_version: snapshot.project_version,
            add_project_ids: [...next].filter((id) => !current.has(id)),
            remove_project_ids: [...current].filter((id) => !next.has(id)),
          },
        })
      } catch (error) {
        if (signal.aborted) return
        if (!(error instanceof ApiError) || ![403, 404, 409].includes(error.status)) {
          busy = false
          for (const control of form.querySelectorAll<
            HTMLInputElement | HTMLButtonElement | HTMLSelectElement
          >('input,button,select'))
            control.disabled = false
          return { field: submit, message: '保存失败，请重试。' }
        }
        toast(
          'warning',
          error.status === 409 ? '归属已变化，请重新打开编辑并确认。' : '文件或项目权限已变化。',
        )
        updated?.(null)
        close()
        await refresh()
        return
      }
      if (signal.aborted) return
      // 移除最后一个可读来源项目后，以重新授权的结果决定抽屉能否继续展示。
      let fresh
      if (updated) {
        fresh = null
        try {
          fresh = driveFile.parse(await request(path, { signal }))
        } catch {
          /* 无法重新核验时关闭抽屉。 */
        }
        if (!fresh && !signal.aborted) {
          updated(null)
          close()
        }
      }
      if (!signal.aborted) {
        await refresh()
        if (!signal.aborted) {
          if (fresh) updated?.(fresh)
          toast('success', '所属项目已保存')
        }
      }
    },
    restoreFocus: () => (trigger.isConnected ? trigger : restoreFocus()),
    onClose: () => signal.removeEventListener('abort', close),
  })
  signal.addEventListener('abort', close, { once: true })
  dialog.addEventListener(
    'cancel',
    (event) => {
      if (busy) {
        event.preventDefault()
        event.stopImmediatePropagation()
      }
    },
    true,
  )
  mountCombo(requiredElement(dialog, '[data-combo]', HTMLElement), { choices, initial: [...current], signal })
  const input = requiredElement(dialog, '[role="combobox"]', HTMLInputElement)
  requiredElement(dialog, '.dialog-footer', HTMLElement).addEventListener('pointerdown', (event) => {
    if (closestControl(event, 'button') && input.getAttribute('aria-expanded') === 'true')
      event.preventDefault()
  })
  input.focus()
}

export function mountFileMetadata(
  dialog: HTMLDialogElement,
  host: HTMLElement,
  row: DriveFile,
  projects: (row: DriveFile) => string,
  edit: (row: DriveFile, opener: HTMLButtonElement, updated: UpdateFile) => Promise<void>,
  signal: AbortSignal,
  category: string,
  size: string,
  projectId: string | null,
) {
  let current = row
  function paint(record: DriveFile | null) {
    if (!record || record.version_id !== row.version_id) {
      dialog.close()
      return
    }
    current = record
    host.innerHTML =
      '<dl class="facts drive-preview-facts"><div><dt>分类</dt><dd>' +
      category +
      '</dd></div><div><dt>来源群</dt><dd>' +
      fileSources(record, projectId) +
      '</dd></div><div><dt>所属项目</dt><dd>' +
      projects(record) +
      '</dd></div><div><dt>大小</dt><dd>' +
      size +
      '</dd></div></dl>'
  }
  paint(current)
  host.addEventListener('click', (event) => {
    const opener = closestControl(event, '[data-drive-projects]')
    if (opener instanceof HTMLButtonElement) void edit(current, opener, paint)
    if (
      closestControl(event, '.drive-source') &&
      !event.metaKey &&
      !event.ctrlKey &&
      !event.altKey &&
      !event.shiftKey
    )
      dialog.close()
  })
  const close = () => dialog.close()
  signal.addEventListener('abort', close, { once: true })
  dialog.addEventListener('close', () => signal.removeEventListener('abort', close), { once: true })
}
