import { z } from '../../api/validation.ts'
import { requiredElement, closestControl } from '../../ui/dom'
import type { Guard } from '../workbench/Account'
import { errorStatus } from '../../api/session-client.ts'
import { overviewSchema, type Project, type ProjectType, type ProjectRequest } from './project-contracts'
import * as workbenchUI from '../../ui/widgets'
const fields: Record<string, string> = {
  name: '项目名称',
  project_type: '项目类型',
  status: '阶段',
  planned_start: '计划开始日期',
  planned_end: '计划结束日期',
  business_timezone: '业务时区',
  background: '项目背景',
}

function profileFields(project?: Project | null) {
  return {
    name: project?.name ?? '',
    project_type: project?.project_type ?? '',
    status: project?.status ?? 'planned',
    planned_start: project?.planned_start ?? '',
    planned_end: project?.planned_end ?? '',
    business_timezone: project?.business_timezone ?? 'Asia/Shanghai',
    background: (project?.background ?? '').replace(/\r\n?/g, '\n'),
  }
}

export function openProjectProfile({
  project,
  projectTypes,
  trigger,
  statuses,
  statusLabel,
  request,
  signal,
  navigation,
  refresh,
}: {
  project?: Project | null
  projectTypes: ProjectType[]
  trigger: HTMLElement
  statuses: string[]
  statusLabel: (s: string) => string
  request: ProjectRequest
  signal: AbortSignal
  navigation: Guard
  refresh: () => void
}) {
  const { escape: esc, openDialog, toast } = workbenchUI
  const creating = !project
  const key = creating ? crypto.randomUUID() : undefined
  const url = '/api/v1/projects' + (project ? '/' + project.id : '')
  let baseline: Record<string, string> = profileFields(project)
  let version = project?.version
  let boardVersion = project?.board_version
  let conflict = false
  let busy = false
  let leaving = false
  let refreshOnClose = false
  let result: { project_id?: string } | undefined
  let archived = false
  let confirmation: HTMLDialogElement | undefined

  const input = (name: string, attrs: string) => `<label class="field pr-form"><span>${fields[name]}</span>
    <input name="${name}" value="${esc(baseline[name])}" ${attrs}></label>`
  const typeOptions = (current: string) => {
    const choices = [{ name: '', tone: 'neutral' }, ...(projectTypes || [])]
    if (current && !choices.some(({ name }) => name === current))
      choices.push({ name: current, tone: 'neutral' })
    return choices
      .map(
        ({ name, tone }) =>
          `<option value="${esc(name)}" data-project-type-tone="${esc(tone)}"${name === current ? ' selected' : ''}>${esc(name || '未分类')}</option>`,
      )
      .join('')
  }
  function control(form: HTMLFormElement, name: string) {
    const field = form.elements.namedItem(name)
    if (
      field instanceof HTMLInputElement ||
      field instanceof HTMLSelectElement ||
      field instanceof HTMLTextAreaElement
    )
      return field
    throw new Error('缺少项目档案字段 ' + name)
  }
  const read = (form: HTMLFormElement) =>
    Object.fromEntries(Object.keys(fields).map((name) => [name, control(form, name).value]))
  const dirty = (form: HTMLFormElement) =>
    Object.keys(fields).some((name) => control(form, name).value !== baseline[name])

  const dialog = openDialog({
    title: creating ? '新建项目' : '项目设置',
    description: project?.name,
    drawer: 'normal',
    variant: 'form',
    confirm: creating ? '新建项目' : '保存设置',
    body: `<fieldset class="project-profile-fields" data-profile-fields>
      ${input('name', 'required maxlength="200"')}
      <label class="field pr-form"><span>${fields.project_type}</span>
        <select class="select project-type-select" name="project_type">${typeOptions(baseline.project_type)}</select>
      </label>
      <label class="field pr-form"><span>${fields.status}</span><select class="select" name="status">
        ${statuses
          .filter((status) => status !== 'archived')
          .map(
            (status) =>
              `<option value="${status}"${status === baseline.status ? ' selected' : ''}>${statusLabel(status)}</option>`,
          )
          .join('')}
      </select></label>
      ${input('planned_start', 'type="date" max="9999-12-31"')}
      ${input('planned_end', 'type="date" max="9999-12-31"')}
      ${input('business_timezone', 'required maxlength="100" spellcheck="false"')}
      <label class="field pr-form"><span>${fields.background}</span><textarea name="background" rows="3" maxlength="4000">\n${esc(baseline.background)}</textarea></label>
    </fieldset>
    <p role="status" data-profile-message hidden></p>
    <section data-profile-conflict hidden>
      <button class="button" type="button" data-profile-reload>读取最新档案</button>
      <div data-profile-latest></div>
    </section>
    ${
      creating
        ? ''
        : `<button class="button" type="button" data-profile-archive aria-haspopup="dialog">归档项目</button>
      <p class="dialog-note" data-profile-archive-hint hidden>请先保存修改，再归档项目。</p>`
    }`,
    isDirty: dirty,
    async onSubmit(form) {
      if (busy || conflict) {
        cancelNavigation()
        return { keepOpen: true }
      }
      const values = read(form)
      const body: Record<string, string | number | undefined> = { ...values }
      if (!values.name.trim()) {
        cancelNavigation()
        return { field: control(form, 'name'), message: '请输入项目名称。' }
      }
      if (values.planned_start && values.planned_end && values.planned_end < values.planned_start) {
        cancelNavigation()
        return { field: control(form, 'planned_end'), message: '计划结束日期应晚于或等于开始日期。' }
      }
      if (!creating) {
        body.expected_version = version
        if (values.business_timezone === baseline.business_timezone) delete body.business_timezone
        else body.expected_board_version = boardVersion
      }
      setBusy(true, '正在保存…')
      try {
        const response = await request(creating ? url : url + '/profile', {
          method: creating ? 'POST' : 'PUT',
          body,
          idempotencyKey: key,
          signal,
        })
        result = creating ? z.object({ project_id: z.string() }).parse(response) : {}
        return undefined
      } catch (error) {
        if (!signal.aborted) {
          cancelNavigation()
          applyError(error)
        }
        return { keepOpen: true }
      } finally {
        setBusy(false)
        if (conflict) reload.focus()
      }
    },
    onClose(confirmed) {
      signal.removeEventListener('abort', onAbort)
      window.removeEventListener('beforeunload', warn)
      delete navigation.hasDraft
      delete navigation.confirmNavigation
      if (signal.aborted) return
      if (archived || (confirmed && result)) {
        toast('success', archived ? '项目已归档。' : creating ? '项目已创建。' : '项目设置已保存。')
        if (leaving) return
        if (creating) location.hash = '#/projects/' + result?.project_id + '/overview'
        else refresh()
      } else if (refreshOnClose) refresh()
    },
    restoreFocus: () => (signal.aborted ? null : trigger),
  })
  const form = requiredElement(dialog, 'form', HTMLFormElement)
  form.autocomplete = 'off'
  const typeSelect = requiredElement(form, '[name=project_type]', HTMLSelectElement)
  const editor = requiredElement(form, '[data-profile-fields]', HTMLFieldSetElement)
  const submit = requiredElement(form, 'button[type=submit]', HTMLButtonElement)
  const message = requiredElement(form, '[data-profile-message]', HTMLElement)
  const conflictSection = requiredElement(form, '[data-profile-conflict]', HTMLElement)
  const reload = requiredElement(form, '[data-profile-reload]', HTMLButtonElement)
  const latest = requiredElement(form, '[data-profile-latest]', HTMLElement)
  const archive = form.querySelector<HTMLButtonElement>('[data-profile-archive]')
  const archiveHint = form.querySelector<HTMLElement>('[data-profile-archive-hint]')

  function show(text = '') {
    message.textContent = text
    message.hidden = !text
  }

  function syncActions() {
    typeSelect.dataset.projectTypeTone = typeSelect.selectedOptions[0]?.dataset.projectTypeTone || 'neutral'
    submit.disabled = busy || conflict
    reload.disabled = busy
    if (archive) {
      archive.disabled = busy || conflict || dirty(form)
      if (archiveHint) archiveHint.hidden = !dirty(form)
    }
  }

  function setBusy(value: boolean, note?: string) {
    busy = value
    editor.disabled = value
    syncActions()
    if (note) show(note)
  }

  function applyError(error: unknown) {
    if ([403, 404].includes(errorStatus(error))) {
      refreshOnClose = true
      toast('warning', '当前账号已无权执行此操作。')
      dialog.close('access-lost')
    } else if (errorStatus(error) === 409 && !creating) {
      conflict = true
      conflictSection.hidden = false
      reload.hidden = false
      latest.replaceChildren()
      show('项目档案或业务时区已更新。请读取最新档案，核对后再提交。')
    } else if (errorStatus(error) === 409) {
      show('此次创建已使用其他项目内容提交。请检查项目列表。')
    } else if (errorStatus(error) === 400) {
      show('请检查项目名称、类型、计划日期和业务时区。时区可填写 Asia/Shanghai 或 UTC。')
    } else {
      show('暂时无法保存，请重试。')
    }
  }

  reload.onclick = async () => {
    setBusy(true, '正在读取最新档案…')
    try {
      const {
        data: { workspace },
      } = z.object({ data: overviewSchema }).parse(await request(url + '/overview', { signal }))
      if (signal.aborted || !dialog.open) return
      if (!workspace.can_manage_profile) {
        if (workspace.project.status === 'archived') {
          refreshOnClose = true
          toast('warning', '项目已归档。')
          dialog.close('archived')
        } else applyError({ status: 403 })
        return
      }
      const values: Record<string, string> = profileFields(workspace.project)
      const typeDraft = typeSelect.value === baseline.project_type ? values.project_type : typeSelect.value
      projectTypes = workspace.project_types || projectTypes
      typeSelect.innerHTML = typeOptions(values.project_type)
      typeSelect.value = typeDraft
      for (const name of Object.keys(fields)) {
        if (name === 'project_type') continue
        const field = control(form, name)
        if (field.value === baseline[name]) field.value = values[name]
      }
      baseline = values
      version = workspace.project.version
      boardVersion = workspace.project.board_version
      latest.innerHTML = `<h3 tabindex="-1">最新档案</h3><dl class="facts">${Object.entries(fields)
        .map(
          ([name, label]) =>
            `<div><dt>${label}</dt><dd>${esc(name === 'status' ? statusLabel(values[name]) : values[name] || '—')}</dd></div>`,
        )
        .join('')}</dl>`
      conflict = false
      reload.hidden = true
      show('已读取最新档案，你修改过的字段已保留。请核对后再提交。')
      requiredElement(latest, 'h3', HTMLElement).focus()
    } catch (error) {
      if (signal.aborted) return
      if ([403, 404].includes(errorStatus(error))) applyError(error)
      else show('暂时无法读取最新档案，请重试。')
    } finally {
      setBusy(false)
    }
  }

  if (archive && project)
    archive.onclick = () => {
      let archiveMessage: HTMLElement
      confirmation = openDialog({
        title: '归档项目？',
        description: project.name,
        variant: 'confirm',
        tone: 'warning',
        confirm: '确认归档',
        body: '<p>归档后将停用项目简报发布，项目档案将停止编辑。有权成员仍可查看项目和历史记录。</p><p role="status" data-archive-message></p>',
        async onSubmit(archiveForm) {
          if (busy) return { keepOpen: true }
          setBusy(true)
          const button = requiredElement(archiveForm, 'button[type=submit]', HTMLButtonElement)
          button.disabled = true
          archiveMessage.textContent = '正在归档…'
          try {
            await request(url + '/archive', { method: 'POST', body: { expected_version: version }, signal })
            archived = true
            return undefined
          } catch (error) {
            if (!signal.aborted && [403, 404, 409].includes(errorStatus(error))) {
              applyError(error)
              confirmation?.close('failed')
            } else if (!signal.aborted) archiveMessage.textContent = '暂时无法归档，请重试。'
            return { keepOpen: true }
          } finally {
            setBusy(false)
            button.disabled = false
          }
        },
        onClose(confirmed) {
          confirmation = undefined
          if (confirmed && dialog.open) dialog.close('confirm')
        },
        restoreFocus: () => (dialog.open ? (conflict ? reload : archive) : null),
      })
      archiveMessage = requiredElement(confirmation, '[data-archive-message]', HTMLElement)
      protectPending(confirmation)
    }

  function protectPending(element: HTMLDialogElement) {
    element.addEventListener(
      'cancel',
      (event) => {
        if (busy) {
          event.preventDefault()
          event.stopImmediatePropagation()
        }
      },
      true,
    )
    element.addEventListener(
      'click',
      (event) => {
        if (busy && (event.target === element || closestControl(event, '[data-dismiss]'))) {
          event.preventDefault()
          event.stopImmediatePropagation()
        }
      },
      true,
    )
  }

  function warn(event: BeforeUnloadEvent) {
    if (busy || dirty(form)) {
      event.preventDefault()
      event.returnValue = ''
    }
  }
  function onAbort() {
    if (confirmation?.open) confirmation.close()
    if (dialog.open) dialog.close()
  }
  function cancelNavigation() {
    if (leaving) dialog.dispatchEvent(new Event('or-dialog-guard-cancel'))
  }
  protectPending(dialog)
  form.addEventListener('input', syncActions)
  syncActions()
  form.addEventListener('invalid', cancelNavigation, true)
  window.addEventListener('beforeunload', warn)
  signal.addEventListener('abort', onAbort, { once: true })
  navigation.hasDraft = () => busy || dirty(form)
  navigation.confirmNavigation = async () => {
    if (busy) return false
    if (!dirty(form)) return true
    leaving = true
    return new Promise<boolean>((resolve) => {
      const finish = (allow: boolean) => {
        dialog.removeEventListener('close', closed)
        dialog.removeEventListener('or-dialog-guard-cancel', cancelled)
        if (!allow) leaving = false
        resolve(allow)
      }
      const closed = () => finish(true)
      const cancelled = () => finish(false)
      dialog.addEventListener('close', closed)
      dialog.addEventListener('or-dialog-guard-cancel', cancelled)
      dialog.dispatchEvent(new Event('cancel', { cancelable: true }))
    })
  }
}
