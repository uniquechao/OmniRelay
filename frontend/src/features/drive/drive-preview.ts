import { requiredElement, closestControl } from '../../ui/dom.ts'
import { z } from '../../api/validation.ts'
import {
  documentPage,
  textPage,
  zipEntries,
  tableRows,
  baseRows,
  officeBlocks,
  versionsPage,
  previewTask,
  previewFailure,
  type DriveFile,
  type Refresh,
  type DriveRequest,
  type PreviewMeta,
  type DocumentBlock,
  type DocumentRun,
  type DocumentImage,
} from './drive-contracts.ts'
import * as workbenchUI from '../../ui/controls.ts'
const messages: Record<string, string> = {
  queued: '预览已排队',
  running: '正在转换预览',
  partial: '本次同步仅取得部分结果，当前显示已保存的版本',
  failed: '文件转换失败',
  source_unavailable: '暂时无法读取来源',
  conversion_busy: '转换服务繁忙',
  forbidden: '已无权查看此文件',
  preview_unavailable: '预览已失效',
  version_unavailable: '此版本已不可用',
  unsupported: '此格式暂不支持预览',
  encrypted: '文件已加密，无法预览',
  over_limit: '文件超出预览处理限制',
  invalid_page: '此页已不可用',
  invalid_sheet: '工作表已不可用',
  invalid_entry: '目录条目已不可用',
  network: '读取预览失败，请检查连接后重试',
}
const recovery: Record<string, string> = {
  encrypted: '请在来源处提供未加密的副本，再查看同步后的版本。',
  unsupported: '请在来源处提供支持预览的格式，再查看同步后的版本。',
  over_limit: '请在来源处拆分文件，再查看同步后的版本。',
}
const retryable = new Set([
  'failed',
  'source_unavailable',
  'conversion_busy',
  'preview_unavailable',
  'network',
])
const esc = workbenchUI.escape
const cell = (value: unknown) =>
  esc(typeof value === 'object' && value !== null ? JSON.stringify(value) : (value ?? ''))
function action(name: string, label: string, disabled = false) {
  return (
    '<button type="button" class="button" data-preview-action="' +
    name +
    '"' +
    (disabled ? ' disabled' : '') +
    '>' +
    label +
    '</button>'
  )
}
function table(rows: unknown[][], fields?: string[]) {
  const header = fields
    ? '<thead><tr>' + fields.map((f) => '<th scope="col">' + cell(f) + '</th>').join('') + '</tr></thead>'
    : ''
  return (
    '<div class="table-box drive-preview-table" tabindex="0" role="region" aria-label="表格预览"><table>' +
    header +
    '<tbody>' +
    rows.map((row) => '<tr>' + row.map((v) => '<td>' + cell(v) + '</td>').join('') + '</tr>').join('') +
    '</tbody></table></div>'
  )
}
// Render only normalized document nodes. Provider HTML and image URLs are never inserted.
export function renderDocumentBlocks(blocks: DocumentBlock[]) {
  let imageIndex = 0
  function runs(items: DocumentRun[] = []) {
    return items
      .map((run) => {
        let text = esc(run.text ?? '')
        if (run.kind === 'person')
          text =
            '<span class="document-person"' +
            (run.unresolved ? ' title="当前未能解析此用户姓名，请检查用户同步或文档读取权限"' : '') +
            '>' +
            text +
            '</span>'
        if (run.code) text = '<code>' + text + '</code>'
        if (run.bold) text = '<strong>' + text + '</strong>'
        if (run.italic) text = '<em>' + text + '</em>'
        if (run.underline) text = '<u>' + text + '</u>'
        if (run.strike) text = '<s>' + text + '</s>'
        const colors = []
        if (run.color !== undefined && run.color >= 1 && run.color <= 7) colors.push('doc-color-' + run.color)
        if (run.background !== undefined && run.background >= 1 && run.background <= 14)
          colors.push('doc-background-' + run.background)
        if (colors.length) text = '<span class="' + colors.join(' ') + '">' + text + '</span>'
        try {
          const link = new URL(run.link ?? '')
          if (['https:', 'http:'].includes(link.protocol))
            text =
              '<a href="' + esc(link.href) + '" target="_blank" rel="noopener noreferrer">' + text + '</a>'
        } catch {}
        return text
      })
      .join('')
  }
  function render(items: DocumentBlock[] | undefined, depth = 0): string {
    if (depth > 40 || !Array.isArray(items)) return ''
    let html = ''
    for (let i = 0; i < items.length; i++) {
      const b = items[i]
      const text = runs(b.runs)
      const cls = ['left', 'center', 'right'].includes(b.align ?? '')
        ? ' class="doc-align-' + b.align + '"'
        : ''
      if (b.type === 'bullet' || b.type === 'ordered') {
        const tag = b.type === 'ordered' ? 'ol' : 'ul'
        html += '<' + tag + '>'
        let j = i
        while (j < items.length && items[j].type === b.type) {
          html += '<li>' + runs(items[j].runs) + render(items[j].children, depth + 1) + '</li>'
          j++
        }
        i = j - 1
        html += '</' + tag + '>'
        continue
      }
      if (b.type === 'image') {
        html +=
          '<figure><canvas data-document-image="' +
          imageIndex++ +
          '" role="img" aria-label="文档图片"></canvas><figcaption class="aux" hidden></figcaption></figure>'
      } else if (b.type === 'table') {
        html +=
          '<div class="table-box drive-preview-table" tabindex="0" role="region" aria-label="文档表格"><table><tbody>' +
          (b.rows ?? [])
            .map(
              (row) =>
                '<tr>' + row.map((cell) => '<td>' + render(cell, depth + 1) + '</td>').join('') + '</tr>',
            )
            .join('') +
          '</tbody></table></div>'
      } else if (b.type === 'divider') html += '<hr>'
      else if (b.type === 'heading') {
        const level = Math.max(1, Math.min(6, Number(b.level) || 1))
        html += '<h' + level + cls + '>' + text + '</h' + level + '>'
      } else if (b.type === 'code') html += '<pre><code>' + text + '</code></pre>'
      else if (b.type === 'quote') html += '<blockquote>' + text + '</blockquote>'
      else if (b.type === 'todo')
        html +=
          '<p' +
          cls +
          '><input type="checkbox" disabled' +
          (b.checked ? ' checked' : '') +
          ' aria-label="待办状态"> ' +
          text +
          '</p>'
      else if (text) html += '<p' + cls + '>' + text + '</p>'
      html += render(b.children, depth + 1)
    }
    return html
  }
  return '<article class="drive-document">' + render(blocks) + '</article>'
}

export async function renderDocumentImages(root: HTMLElement, data: unknown, signal: AbortSignal) {
  const images: (DocumentImage | undefined)[] = []
  function visit(blocks: DocumentBlock[] | undefined, depth = 0) {
    if (depth > 40 || !Array.isArray(blocks)) return
    for (const b of blocks) {
      if (b.type === 'image') images.push(b.image)
      if (b.type === 'table') for (const row of b.rows ?? []) for (const cell of row) visit(cell, depth + 1)
      visit(b.children, depth + 1)
    }
  }
  const page = documentPage.safeParse(data)
  if (!page.success) return
  visit(page.data.document)
  for (const canvas of root.querySelectorAll<HTMLCanvasElement>('[data-document-image]')) {
    if (signal?.aborted) return
    const image = images[Number(canvas.dataset.documentImage)]
    try {
      if (
        !image ||
        !['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(image?.mime) ||
        typeof image.data !== 'string'
      )
        throw new Error()
      const bytes = Uint8Array.from(atob(image.data), (c) => c.charCodeAt(0))
      const bitmap = await createImageBitmap(new Blob([bytes], { type: image.mime }))
      try {
        if (signal?.aborted || !canvas.isConnected) return
        if (bitmap.width * bitmap.height > 40000000) throw new Error()
        canvas.width = bitmap.width
        canvas.height = bitmap.height
        canvas.getContext('2d')?.drawImage(bitmap, 0, 0)
      } finally {
        bitmap.close()
      }
    } catch {
      canvas.hidden = true
      const caption = canvas.nextElementSibling
      if (!(caption instanceof HTMLElement)) continue
      caption.textContent = '此图片暂时无法显示，请重新同步文档后重试。'
      caption.hidden = false
    }
  }
}

export function renderResult(kind: string, data: unknown, meta: PreviewMeta) {
  if (data && typeof data === 'object' && 'document' in data)
    return renderDocumentBlocks(documentPage.parse(data).document)
  const paragraph = (value: string) => '<p class="drive-preview-text">' + esc(value) + '</p>'
  if (data && typeof data === 'object' && 'text' in data)
    return paragraph(textPage.parse(data).text || '本页没有正文')
  if (!Array.isArray(data)) throw new Error('文件预览响应无效。')
  if (!data.length) return paragraph(kind === 'zip' ? '目录为空' : '本页没有内容')
  if (kind === 'zip')
    return (
      '<ul class="drive-archive">' +
      zipEntries
        .parse(data)
        .map(
          (item) =>
            '<li>' +
            (item.state === 'ready'
              ? action('entry-' + Number(item.entry), esc(item.name))
              : esc(item.name) + ' · ' + (item.directory ? '目录' : messages[item.state] || '无法预览')) +
            '</li>',
        )
        .join('') +
      '</ul>'
    )
  if (kind === 'base') {
    if (!meta.fields) throw new Error('缺少数据表字段。')
    const fields = meta.fields
    return table(
      baseRows.parse(data).map((row) => fields.map((field) => row.fields[field])),
      fields,
    )
  }
  if (kind === 'xlsx') return table(tableRows.parse(data))
  return officeBlocks
    .parse(data)
    .map((block) => (block.table ? table(block.table) : paragraph(block.text ?? '')))
    .join('')
}

export async function previewCanvas(data: Blob, label: string) {
  const bitmap = await createImageBitmap(data)
  try {
    const canvas = document.createElement('canvas')
    canvas.width = bitmap.width
    canvas.height = bitmap.height
    canvas.setAttribute('role', 'img')
    canvas.setAttribute('aria-label', label)
    canvas.getContext('2d')?.drawImage(bitmap, 0, 0)
    return canvas
  } finally {
    bitmap.close()
  }
}

export function openFilePreview(
  row: DriveFile,
  refresh: Refresh,
  lifetime: AbortSignal,
  {
    request,
    mountMetadata,
    restoreFocus,
    filePath,
  }: {
    request: DriveRequest
    mountMetadata: (dialog: HTMLDialogElement, host: HTMLElement) => void
    restoreFocus: () => HTMLElement | null
    filePath: (path: string) => string
  },
) {
  const dialog = workbenchUI.openDialog({
    title: row.name,
    drawer: 'wide',
    restoreFocus,
    description:
      row.format +
      (row.category === 'docs' && row.version_id ? ' · 已同步正文' : '') +
      (row.version_id ? ' · 版本 ' + row.version_id : ' · 尚无可读版本'),
    body:
      '<div data-metadata></div><section class="drive-preview-content"><p role="status" tabindex="-1"></p>' +
      '<div data-preview-controls></div><div data-preview-body></div></section>',
  })
  mountMetadata(dialog, requiredElement(dialog, '[data-metadata]', HTMLElement))
  const status = requiredElement(dialog, '[role="status"]', HTMLElement)
  const controls = requiredElement(dialog, '[data-preview-controls]', HTMLElement)
  const body = requiredElement(dialog, '[data-preview-body]', HTMLElement)
  const versions = '/api/v1/feishu/files/' + encodeURIComponent(row.id) + '/versions'
  const versionPath = versions + '/' + encodeURIComponent(String(row.version_id))
  const root = versionPath + '/previews'
  let page = 1,
    sheet = 0,
    entry = 0,
    directoryPage = 1
  let pending: AbortController | undefined
  let displayedTask: { path: string; signal: AbortSignal; checking?: boolean; media?: boolean } | null = null
  async function recheck() {
    const task = displayedTask
    if (!task || task.checking || !dialog.open || document.hidden) return
    task.checking = true
    try {
      const result = await request(task.path, { signal: task.signal })
      if (task !== displayedTask || task.signal.aborted) return
      if (task.media) {
        if (!versionsPage.parse(result).items.some((v) => String(v.id) === String(row.version_id)))
          showFailure('forbidden')
        return
      }
      const current = previewTask.parse(result)
      if (current.stage !== 'ready') showFailure(current.stage)
      else if (String(current.version_id) !== String(row.version_id)) showFailure('version_unavailable')
    } catch (error) {
      const failure = previewFailure.safeParse(error).data
      if (task !== displayedTask || task.signal.aborted) return
      if (failure?.data?.error === 'preview_unavailable') {
        // 短期缓存到期，按当前页和版本重新鉴权并读取。
        void load()
      } else {
        const code = failure?.data?.error
        showFailure(
          [403, 404].includes(failure?.status ?? 0)
            ? 'forbidden'
            : code && Object.hasOwn(messages, code)
              ? code
              : 'network',
        )
      }
    } finally {
      task.checking = false
    }
  }
  const recheckTimer = setInterval(recheck, 30000)
  window.addEventListener('focus', recheck)
  document.addEventListener('visibilitychange', recheck)
  dialog.addEventListener(
    'close',
    () => {
      clearInterval(recheckTimer)
      window.removeEventListener('focus', recheck)
      document.removeEventListener('visibilitychange', recheck)
      pending?.abort()
    },
    { once: true },
  )

  function showFailure(code: string) {
    displayedTask = null
    if (controls.contains(document.activeElement) || body.contains(document.activeElement)) status.focus()
    body.replaceChildren()
    controls.innerHTML = entry ? action('directory', '返回目录') : ''
    status.textContent = messages[code] || messages.failed
    if (Object.hasOwn(recovery, code)) {
      status.textContent += '。' + recovery[code]
      controls.insertAdjacentHTML('beforeend', action('refresh-list', '返回并刷新列表'))
    }
    if (retryable.has(code)) controls.insertAdjacentHTML('beforeend', action('retry', '重试'))
    if (code === 'forbidden') {
      requiredElement(dialog, '[data-metadata]', HTMLElement).replaceChildren()
      requiredElement(dialog, '.dialog-heading h2', HTMLElement).textContent = '文件预览'
      requiredElement(dialog, '.dialog-heading p', HTMLElement).textContent = ''
      controls.replaceChildren()
      void refresh()
    }
  }
  async function load() {
    displayedTask = null
    pending?.abort()
    pending = new AbortController()
    const signal = AbortSignal.any([pending.signal, lifetime])
    const params = new URLSearchParams({ page: String(page) })
    if (sheet) params.set('sheet', String(sheet))
    if (entry) params.set('entry', String(entry))
    if (controls.contains(document.activeElement) || body.contains(document.activeElement)) status.focus()
    body.replaceChildren()
    controls.innerHTML = entry ? action('directory', '返回目录') : ''
    status.textContent = messages.queued
    try {
      if (row.format_key === 'image' || row.format_key === 'video') {
        const media = document.createElement(row.format_key === 'image' ? 'img' : 'video')
        const available = versionsPage.parse(await request(versions, { signal }))
        signal.throwIfAborted()
        if (!available.items.some((v) => String(v.id) === String(row.version_id))) {
          showFailure('forbidden')
          return
        }
        media.addEventListener('error', () => {
          if (signal.aborted || !dialog.open) return
          status.textContent = '暂时无法预览此格式，可返回文件列表下载原件。'
          void recheck()
        })
        if (media instanceof HTMLImageElement) media.alt = row.name
        else {
          media.controls = true
          media.preload = 'metadata'
          media.setAttribute('aria-label', row.name)
        }
        displayedTask = { path: versions, signal, media: true }
        media.src = filePath(versionPath + '/content')
        body.replaceChildren(media)
        status.textContent = ''
        return
      }
      let task = previewTask.parse(await request(root + '?' + params, { method: 'POST', signal }))
      signal.throwIfAborted()
      const path = root + '/' + encodeURIComponent(task.task_id)
      while (task.stage === 'queued' || task.stage === 'running') {
        status.textContent = messages[task.stage]
        await new Promise((resolve) => setTimeout(resolve, 700))
        signal.throwIfAborted()
        task = previewTask.parse(await request(path, { signal }))
        signal.throwIfAborted()
      }
      if (!dialog.open) return
      if (task.stage !== 'ready') {
        showFailure(task.stage)
        return
      }
      if (String(task.version_id) !== String(row.version_id)) {
        showFailure('version_unavailable')
        return
      }
      const meta = task.meta
      if (!meta) throw new Error('文件预览元数据缺失。')
      const data = await request(path + '/result', { signal, acceptPNG: true })
      signal.throwIfAborted()
      const kind = entry
        ? (meta.name?.split('.').pop()?.toLowerCase() ?? '')
        : row.category === 'files'
          ? (row.name.split('.').pop()?.toLowerCase() ?? '')
          : row.format_key
      if (data instanceof Blob) {
        const canvas = await previewCanvas(data, 'PDF 第 ' + page + ' 页')
        if (signal.aborted || !dialog.open) return
        body.replaceChildren(canvas)
      } else {
        body.innerHTML = renderResult(kind, data, meta)
        await renderDocumentImages(body, data, signal)
        signal.throwIfAborted()
      }
      displayedTask = { path, signal, checking: false }
      if (row.category === 'docs')
        requiredElement(dialog, '.dialog-heading p', HTMLElement).textContent =
          row.format +
          (meta.document_format === 'rich' ? ' · 含格式与图片' : ' · 历史纯文本版本') +
          ' · 版本 ' +
          row.version_id
      const summary = []
      if (row.sync_state === 'partial') summary.push(messages.partial)
      if (entry) summary.push(meta.name)
      summary.push('第 ' + meta.page + ' / ' + meta.pages + ' 页')
      if (meta.total && meta.total > 0)
        summary.push('显示 ' + meta.first + '–' + meta.last + '，共 ' + meta.total)
      status.textContent = summary.join(' · ')
      if (
        kind === 'zip' &&
        zipEntries.parse(data).some((item) => !item.directory && Object.hasOwn(recovery, item.state))
      ) {
        controls.insertAdjacentHTML('beforeend', action('refresh-list', '返回并刷新列表'))
      }
      if (meta.sheets?.length)
        controls.insertAdjacentHTML(
          'beforeend',
          '<label>工作表 <select data-sheet>' +
            meta.sheets
              .map(
                (name, i) =>
                  '<option value="' +
                  i +
                  '"' +
                  (i === meta.sheet ? ' selected' : '') +
                  '>' +
                  esc(name) +
                  '</option>',
              )
              .join('') +
            '</select></label>',
        )
      if (meta.pages && meta.pages > 1)
        controls.insertAdjacentHTML(
          'beforeend',
          action('prev', '上一页', page === 1) + action('next', '下一页', page === meta.pages),
        )
    } catch (error) {
      const failure = previewFailure.safeParse(error).data
      if (signal.aborted || !dialog.open) return
      const code = failure?.data?.error
      showFailure(
        [403, 404].includes(failure?.status ?? 0)
          ? 'forbidden'
          : code && Object.hasOwn(messages, code)
            ? code
            : 'network',
      )
    }
  }
  dialog.addEventListener('click', async (event) => {
    const name = closestControl(event, '[data-preview-action]')?.dataset.previewAction
    if (!name) return
    if (name === 'refresh-list') {
      const trigger = closestControl(event, 'button')
      if (trigger instanceof HTMLButtonElement) trigger.disabled = true
      try {
        await refresh()
      } finally {
        if (dialog.open) dialog.close()
      }
      return
    }
    if (name === 'prev') page--
    if (name === 'next') page++
    if (name.startsWith('entry-')) {
      directoryPage = page
      entry = Number(name.slice(6))
      page = 1
      sheet = 0
    }
    if (name === 'directory') {
      entry = sheet = 0
      page = directoryPage
    }
    void load()
  })
  controls.addEventListener('change', (event) => {
    if (!(event.target instanceof HTMLSelectElement) || !event.target.matches('[data-sheet]')) return
    sheet = Number(event.target.value)
    page = 1
    void load()
  })
  if (row.version_id) void load()
  else status.textContent = '文件仅部分同步，尚无可读版本。'
}

export async function readLocalDocument(
  path: string,
  request: DriveRequest,
  trigger: HTMLButtonElement,
  signal: AbortSignal,
  refresh: Refresh,
) {
  const { openDialog, toast } = workbenchUI
  trigger.disabled = true
  try {
    const source = z.object({ title: z.string(), text: z.string() }).parse(await request(path, { signal }))
    if (signal.aborted || !trigger.isConnected) return
    if (trigger.hasAttribute('data-local-download')) {
      const link = document.createElement('a')
      link.href = URL.createObjectURL(new Blob([source.text], { type: 'text/plain;charset=utf-8' }))
      link.download = source.title + '.txt'
      link.click()
      setTimeout(() => URL.revokeObjectURL(link.href), 1000)
      return
    }
    const close = () => dialog.close()
    const dialog = openDialog({
      title: source.title,
      drawer: 'wide',
      description: '项目文档',
      body:
        '<div class="drive-preview-content"><p class="drive-preview-text">' +
        esc(source.text || '暂无正文。') +
        '</p></div>',
      restoreFocus: () => trigger,
      onClose: () => signal.removeEventListener('abort', close),
    })
    signal.addEventListener('abort', close, { once: true })
  } catch (error) {
    const failure = previewFailure.safeParse(error).data
    if (signal.aborted) return
    const denied = [403, 404].includes(failure?.status ?? 0)
    toast('error', denied ? '当前文档已不可访问。' : '读取文档失败，请重试。')
    if (denied) await refresh()
  } finally {
    trigger.disabled = false
  }
}
