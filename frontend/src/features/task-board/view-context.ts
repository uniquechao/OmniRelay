import { useLayoutEffect, useRef } from 'react'

export type ViewContextScope = { userId: string; page: string; objectId?: string | null; region: string; ready?: boolean; initialScroll?: 'top' | 'end' }
type Position = { top: number; left: number; trigger?: string }
const prefix = 'omni-grid.view.'
const memory = new Map<string, Position>()
const finite = (value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0
function read(key: string): Position | undefined {
  try {
    const raw = sessionStorage.getItem(key)
    if (raw) {
      const value = JSON.parse(raw) as Position
      if (finite(value.top) && finite(value.left)) return { top: value.top, left: value.left, trigger: typeof value.trigger === 'string' && value.trigger.length <= 120 ? value.trigger : undefined }
    }
  } catch { /* 禁用或损坏的存储回退会话内位置。 */ }
  return memory.get(key)
}
function write(key: string, position: Position) {
  memory.delete(key); memory.set(key, position)
  if (memory.size > 100) memory.delete(memory.keys().next().value!)
  try {
    sessionStorage.setItem(key, JSON.stringify(position))
    const keys = Object.keys(sessionStorage).filter(item => item.startsWith(prefix))
    for (const old of keys.slice(0, Math.max(0, keys.length - 100))) if (old !== key) sessionStorage.removeItem(old)
  } catch { /* 位置恢复不依赖浏览器存储授权。 */ }
}

/** 只存有限坐标和显式data-view-trigger标识；异步内容就绪后传ready或调用restore。 */
export function useViewContext<T extends HTMLElement = HTMLDivElement>({ userId, page, objectId = null, region, ready = true, initialScroll = 'top' }: ViewContextScope) {
  const ref = useRef<T>(null)
  const restoreRef = useRef<() => void>(() => {})
  const key = prefix + JSON.stringify([userId, page, objectId, region])
  useLayoutEffect(() => {
    const element = ref.current
    if (!element || !ready) return
    let position: Position | undefined = read(key) ?? (initialScroll === 'end' ? { top: Math.max(0, element.scrollHeight - element.clientHeight), left: 0 } : undefined)
    let trigger = position?.trigger
    let restoring = position !== undefined
    const restore = () => {
      element.scrollTop = Math.min(position?.top ?? 0, Math.max(0, element.scrollHeight - element.clientHeight))
      element.scrollLeft = Math.min(position?.left ?? 0, Math.max(0, element.scrollWidth - element.clientWidth))
      if (element.scrollTop === (position?.top ?? 0) && element.scrollLeft === (position?.left ?? 0)) restoring = false
      if (trigger) Array.from(element.querySelectorAll<HTMLElement>('[data-view-trigger]')).find(node => node.dataset.viewTrigger === trigger && !node.closest('[hidden],[inert]'))?.focus({ preventScroll: true })
    }
    restoreRef.current = restore
    const capture = () => { if (!restoring) { position = { top: Math.max(0, element.scrollTop), left: Math.max(0, element.scrollLeft), trigger }; write(key, position) } }
    const interact = () => { restoring = false }
    const focus = (event: FocusEvent) => {
      const value = (event.target as HTMLElement).closest<HTMLElement>('[data-view-trigger]')?.dataset.viewTrigger
      if (value && value.length <= 120) { trigger = value; capture() }
    }
    const clamp = () => {
      if (restoring) { restore(); return }
      element.scrollTop = Math.min(element.scrollTop, Math.max(0, element.scrollHeight - element.clientHeight))
      element.scrollLeft = Math.min(element.scrollLeft, Math.max(0, element.scrollWidth - element.clientWidth))
    }
    restore()
    const frame = requestAnimationFrame(restore)
    const observer = new ResizeObserver(clamp); observer.observe(element)
    const mutations = new MutationObserver(clamp); mutations.observe(element, { childList: true, subtree: true, characterData: true })
    for (const type of ['pointerdown', 'keydown', 'wheel', 'touchstart']) element.addEventListener(type, interact, { passive: true })
    element.addEventListener('scroll', capture, { passive: true }); element.addEventListener('focusin', focus)
    window.addEventListener('pagehide', capture); window.addEventListener('pageshow', restore)
    return () => {
      cancelAnimationFrame(frame); observer.disconnect(); mutations.disconnect()
      for (const type of ['pointerdown', 'keydown', 'wheel', 'touchstart']) element.removeEventListener(type, interact)
      element.removeEventListener('scroll', capture); element.removeEventListener('focusin', focus)
      window.removeEventListener('pagehide', capture); window.removeEventListener('pageshow', restore)
    }
  }, [key, ready, initialScroll])
  return { ref, restore: () => restoreRef.current() }
}
