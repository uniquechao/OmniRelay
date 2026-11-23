import { requiredElement, closestControl } from '../../ui/dom'
import type { Candidate } from './project-contracts'
import * as workbenchUI from '../../ui/widgets'
export function memberPicker() {
  return `<div class="field member-picker">
    <label for="member-search">选择人员</label>
    <div class="member-selected" aria-label="已选人员" role="group" hidden></div>
    <div class="dev-combo">
      <div class="member-search-control">
        <svg class="icon" aria-hidden="true"><use href="#chat-search-icon"/></svg>
        <input id="member-search" type="search" role="combobox" name="user_query" placeholder="搜索姓名或账号" autocomplete="off" maxlength="200" aria-autocomplete="list" aria-expanded="false" aria-controls="project-user-options">
        <span class="member-search-picked" role="status"></span>
      </div>
      <div class="combo-box" data-popup hidden>
        <div id="project-user-options" role="listbox" aria-multiselectable="true" aria-label="可添加的人员"></div>
        <div class="member-search-status" role="status" hidden></div>
        <button class="button" type="button" data-retry hidden>重试</button>
        <div class="user-choice-pages" hidden><button type="button" data-previous>上一页</button><button type="button" data-more>下一页</button></div>
      </div>
    </div>
  </div>`
}

export function mountMemberPicker(
  dialog: HTMLDialogElement,
  find: (query: string, page: number) => Promise<Candidate[]>,
  select: (users: Candidate[]) => void,
) {
  const { escape, personAvatar } = workbenchUI
  const box = requiredElement(dialog, '.member-picker', HTMLElement)
  const input = requiredElement(box, 'input', HTMLInputElement),
    popup = requiredElement(box, '[data-popup]', HTMLElement)
  const list = requiredElement(box, '[role=listbox]', HTMLElement),
    status = requiredElement(box, '.member-search-status', HTMLElement)
  const pages = requiredElement(box, '.user-choice-pages', HTMLElement),
    retry = requiredElement(box, '[data-retry]', HTMLButtonElement)
  const chosen = new Map<string, Candidate>()
  const chips = requiredElement(box, '.member-selected', HTMLElement)
  let page = 1,
    revision = 0,
    active = -1,
    items: Candidate[] = [],
    timer: number | undefined

  function highlight(index: number) {
    list.children[active]?.removeAttribute('data-active')
    active = index
    const row = list.children[index]
    if (!(row instanceof HTMLElement)) {
      input.removeAttribute('aria-activedescendant')
      return
    }
    row.dataset.active = 'true'
    input.setAttribute('aria-activedescendant', row.id)
    row.scrollIntoView({ block: 'nearest' })
  }
  function close() {
    revision++
    clearTimeout(timer)
    popup.hidden = true
    input.setAttribute('aria-expanded', 'false')
    highlight(-1)
  }
  function search(next = 1, delay = 0, initial?: 'first' | 'last') {
    const ticket = ++revision
    clearTimeout(timer)
    page = next
    items = []
    highlight(-1)
    list.replaceChildren()
    pages.hidden = retry.hidden = true
    popup.hidden = status.hidden = false
    status.textContent = '正在搜索…'
    input.setAttribute('aria-expanded', 'true')
    const query = input.value.trim()
    timer = window.setTimeout(async () => {
      try {
        const result = await find(query, next)
        if (ticket !== revision || !dialog.open) return
        items = result
        list.innerHTML = result
          .map((user, i) => {
            const detail =
              user.username && user.username !== user.display_name
                ? '<small>' + escape(user.username) + '</small>'
                : ''
            return `<div role="option" class="combo-option member-option" id="member-option-${i}" data-user-choice="${i}" aria-selected="${chosen.has(user.account_id)}">
            ${personAvatar(user.display_name, user.account_id)}<span>${escape(user.display_name)}</span>${detail}<span class="member-option-check" aria-hidden="true">✓</span></div>`
          })
          .join('')
        list.scrollTop = 0
        status.hidden = result.length > 0
        status.textContent = result.length ? '' : '没有可添加的匹配人员'
        pages.hidden = next === 1 && result.length < 25
        requiredElement(pages, '[data-previous]', HTMLButtonElement).disabled = next === 1
        requiredElement(pages, '[data-more]', HTMLButtonElement).disabled = result.length < 25
        if (initial && result.length) highlight(initial === 'last' ? result.length - 1 : 0)
      } catch {
        if (ticket !== revision || !dialog.open) return
        status.textContent = '人员列表加载失败'
        retry.hidden = false
      }
    }, delay)
  }
  function renderSelection() {
    const users = [...chosen.values()]
    chips.hidden = !users.length
    chips.innerHTML = users
      .map(
        (user) =>
          `<button type="button" data-remove-choice="${escape(user.account_id)}" aria-label="移除已选人员 ${escape(user.display_name)}">${escape(user.display_name)}<svg class="icon" aria-hidden="true"><use href="#action-close"/></svg></button>`,
      )
      .join('')
    requiredElement(box, '.member-search-picked', HTMLElement).textContent = users.length
      ? `已选 ${users.length} 人`
      : ''
    for (const row of list.children)
      row.setAttribute(
        'aria-selected',
        String(chosen.has(items[Number(row.getAttribute('data-user-choice'))].account_id)),
      )
    select(users)
    input.dispatchEvent(new Event('change', { bubbles: true }))
  }
  function pick(index: number) {
    const user = items[index]
    if (!user || input.matches(':disabled')) return
    if (chosen.has(user.account_id)) chosen.delete(user.account_id)
    else chosen.set(user.account_id, user)
    input.focus()
    highlight(index)
    renderSelection()
  }
  chips.onclick = (event) => {
    const chip = closestControl(event, '[data-remove-choice]')
    if (!chip || input.matches(':disabled')) return
    chosen.delete(chip.dataset.removeChoice ?? '')
    input.focus()
    renderSelection()
  }
  input.onclick = () => {
    if (popup.hidden) search()
  }
  input.oninput = () => search(1, 180)
  input.onkeydown = (event) => {
    if (event.isComposing) return
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const down = event.key === 'ArrowDown'
      if (popup.hidden) search(1, 0, down ? 'first' : 'last')
      else if (items.length)
        highlight(down ? (active + 1) % items.length : active <= 0 ? items.length - 1 : active - 1)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      if (popup.hidden) search()
      else if (items.length) pick(active < 0 ? 0 : active)
    }
  }
  box.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !popup.hidden && !event.isComposing) {
      event.preventDefault()
      event.stopPropagation()
      close()
      input.focus()
    }
  })
  list.onmousedown = (event) => event.preventDefault()
  list.onclick = (event) => {
    const row = closestControl(event, '[data-user-choice]')
    if (row) pick(Number(row.getAttribute('data-user-choice')))
  }
  requiredElement(pages, '[data-previous]', HTMLButtonElement).onclick = () => {
    input.focus()
    search(page - 1)
  }
  requiredElement(pages, '[data-more]', HTMLButtonElement).onclick = () => {
    input.focus()
    search(page + 1)
  }
  retry.onclick = () => {
    input.focus()
    search(page)
  }
  box.addEventListener('focusout', (event) => {
    if (!box.contains(event.relatedTarget instanceof Node ? event.relatedTarget : null)) close()
  })
  dialog.addEventListener('pointerdown', (event) => {
    if (!box.contains(event.target instanceof Node ? event.target : null)) close()
  })
  dialog.addEventListener('close', close, { once: true })
}
