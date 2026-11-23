import { useCallback, useSyncExternalStore, type RefObject } from 'react'

export function inheritsFieldsetDisabled(element: HTMLElement | null): boolean {
  for (let parent = element?.parentElement; parent; parent = parent.parentElement) {
    if (parent.tagName !== 'FIELDSET' || !(parent as HTMLFieldSetElement).disabled) continue
    const legend = Array.from(parent.children).find(child => child.tagName === 'LEGEND')
    if (!legend?.contains(element)) return true
  }
  return false
}

export function useFieldsetDisabled(ref: RefObject<HTMLElement | null>): boolean {
  const subscribe = useCallback((changed: () => void) => {
    const observer = new MutationObserver(changed)
    for (let parent = ref.current?.parentElement; parent; parent = parent.parentElement) {
      if (parent.tagName === 'FIELDSET') observer.observe(parent, { attributes: true, attributeFilter: ['disabled'], childList: true, subtree: true })
    }
    return () => observer.disconnect()
  }, [ref])
  const snapshot = useCallback(() => inheritsFieldsetDisabled(ref.current), [ref])
  return useSyncExternalStore(subscribe, snapshot, () => false)
}
