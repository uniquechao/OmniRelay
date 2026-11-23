import { useCallback, useMemo, useSyncExternalStore, type Dispatch, type SetStateAction } from 'react'

const changeEvent = 'omni-grid:hash-params-change'
function subscribe(listener: () => void) {
  window.addEventListener('hashchange', listener)
  window.addEventListener(changeEvent, listener)
  return () => { window.removeEventListener('hashchange', listener); window.removeEventListener(changeEvent, listener) }
}
function snapshot() { return window.location.hash }

export function updateHashParams(patch: Record<string, string | null>, targetRoute?: string) {
  const [rawRoute, search] = window.location.hash.split('?')
  const route = targetRoute ?? (rawRoute || '#/')
  const params = new URLSearchParams(search)
  for (const [key, value] of Object.entries(patch)) { if (value === null || value === '') params.delete(key); else params.set(key, value) }
  const next = !targetRoute && !rawRoute && !params.size ? '' : `${route}${params.size ? `?${params}` : ''}`
  if (next === window.location.hash) return
  if (!window.dispatchEvent(new CustomEvent('omni-grid:before-route-change', { cancelable: true, detail: { hash: next } }))) return
  replaceTaskHash(next)
}

export function replaceTaskHash(next:string) {
  window.history.replaceState(history.state, '', next)
  window.dispatchEvent(new Event(changeEvent))
}

export function useHashSnapshot() { return useSyncExternalStore(subscribe, snapshot, () => '') }

type Scalar = string | number | boolean
function parse<T extends Scalar>(raw: string | null, fallback: T): T {
  if (raw === null) return fallback
  if (typeof fallback === 'number') { const value = Number(raw); return (Number.isSafeInteger(value) && value > 0 ? value : fallback) as T }
  if (typeof fallback === 'boolean') return (raw === 'true') as T
  return raw as T
}

export function useHashState<T extends Scalar>(key: string, fallback: T, allowed?: readonly T[]): [T, Dispatch<SetStateAction<T>>] {
  const hash = useSyncExternalStore(subscribe, snapshot, () => '')
  const raw = parse(new URLSearchParams(hash.split('?')[1]).get(key), fallback)
  const value = allowed && !allowed.includes(raw) ? fallback : raw
  const route = hash.split('?')[0] || '#/'
  const setValue = useCallback<Dispatch<SetStateAction<T>>>((action) => {
    if ((window.location.hash.split('?')[0] || '#/') !== route) return
    const current = parse(new URLSearchParams(window.location.hash.split('?')[1]).get(key), fallback)
    const next = typeof action === 'function' ? action(current) : action
    updateHashParams({ [key]: next === fallback ? null : String(next) })
  }, [key, fallback, route])
  return [value, setValue]
}

export function useHashFilters<T extends Record<string, Scalar>>(defaults: T): [T, Dispatch<SetStateAction<T>>] {
  const hash = useSyncExternalStore(subscribe, snapshot, () => '')
  const route = hash.split('?')[0] || '#/'
  const decode = useCallback((value: string) => {
    const params = new URLSearchParams(value.split('?')[1])
    return Object.fromEntries(Object.entries(defaults).map(([key, fallback]) => [key, parse(params.get(`filter_${key}`), fallback)])) as T
  }, [defaults])
  const params = new URLSearchParams(hash.split('?')[1])
  const filterSnapshot = JSON.stringify(Object.keys(defaults).map(key => params.get(`filter_${key}`)))
  const value = useMemo(() => {
    const values = JSON.parse(filterSnapshot) as (string | null)[]
    return Object.fromEntries(Object.entries(defaults).map(([key, fallback], index) => [key, parse(values[index], fallback)])) as T
  }, [defaults, filterSnapshot])
  const setValue = useCallback<Dispatch<SetStateAction<T>>>((action) => {
    if ((window.location.hash.split('?')[0] || '#/') !== route) return
    const next = typeof action === 'function' ? action(decode(window.location.hash)) : action
    updateHashParams(Object.fromEntries(Object.entries(next).map(([key, value]) => [`filter_${key}`, value === defaults[key] ? null : String(value)])))
  }, [decode, defaults, route])
  return [value, setValue]
}
