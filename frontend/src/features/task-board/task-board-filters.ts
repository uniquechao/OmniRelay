import { taskDate, taskDateInstant } from './task-dates'
import type { TaskSort } from './api'
export function taskTagIds(raw: string, legacy: string) {
  if (!raw) return legacy ? [legacy] : []
  try { const value: unknown = JSON.parse(raw); return Array.isArray(value) && value.every(item => typeof item === 'string') ? [...new Set(value as string[])] : [] } catch { return [] }
}
export function boardQuery(filters: { q: string; sort: TaskSort; owner: string; status: string; column: string; tags: string[]; overdue: boolean; dueFrom: string; dueBefore: string; due: string; timezone?: string | null; priority?: string }, now = new Date()) {
  const query = new URLSearchParams({ q: filters.q, sort: filters.sort, limit: '50' })
  if (filters.owner === 'unassigned') query.set('owner_unassigned', 'true')
  else if (filters.owner) query.set('owner_member_id', filters.owner)
  if (filters.status) query.set('status', filters.status)
  if (filters.column) query.set('filter_column_id', filters.column)
  if (filters.priority === 'unset') query.set('priority_unset', 'true')
  else if (filters.priority) query.set('priority', filters.priority)
  filters.tags.forEach(id => query.append('tag_ids', id))
  if (filters.tags.length) query.set('tag_match', 'any')
  if (filters.due === 'none') query.set('no_due', 'true')
  else if (filters.due === 'overdue' || filters.overdue) query.set('overdue_only', 'true')
  else if (['today', 'week'].includes(filters.due)) {
    const timezone = filters.timezone || 'UTC'
    const date = taskDate(now.toISOString(), timezone)
    const end = new Date(Date.parse(`${date}T12:00:00Z`) + (filters.due === 'week' ? 8 : 1) * 86400000).toISOString().slice(0, 10)
    query.set('due_from', taskDateInstant(date, timezone))
    query.set('due_before', taskDateInstant(end, timezone))
  } else {
    // 旧UTC精确范围深链保持原语义；新相对期限按项目业务时区，缺失时兼容UTC。
    if (filters.dueFrom) query.set('due_from', `${filters.dueFrom}T00:00:00Z`)
    if (filters.dueBefore) query.set('due_before', `${filters.dueBefore}T00:00:00Z`)
  }
  return query
}
