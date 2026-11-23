import type { MouseEvent } from 'react'

export function openTaskDatePicker(event: MouseEvent<HTMLLabelElement>) {
  const input = event.currentTarget.querySelector('input')
  if (!input || input.disabled || input.readOnly) return
  if (event.target !== input) { event.preventDefault(); input.focus() }
  if (typeof input.showPicker === 'function') {
    try { input.showPicker() } catch { /* Native segmented input remains usable when a browser cannot open it. */ }
  }
}
