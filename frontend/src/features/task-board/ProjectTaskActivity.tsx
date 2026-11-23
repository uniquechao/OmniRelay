import { useProjectRevision } from './project-state'
import { useEffect, useState } from 'react'
import { Button } from './controls'
import {
  getTaskActivity,
  type ProjectDetail,
  type TaskActivityPage,
  type TaskTag,
} from './api'

const labels: Record<string, string> = {
  title: '标题',
  description: '正文',
  owner_member_id: '负责人',
  participant_ids: '参与人',
  priority: '优先级',
  start_at: '开始时间',
  due_at: '截止时间',
  tag_ids: '标签',
  status: '状态',
  column_id: '所在列',
  blocked_reason: '受阻原因',
  completion_note: '完成结果',
  change_reason: '变更原因',
  acceptance: '验收条件',
}
const states: Record<string, string> = {
  todo: '待开始',
  in_progress: '进行中',
  blocked: '受阻',
  done: '已完成',
  cancelled: '已取消',
}
export function ProjectTaskActivity({
  project,
  taskId,
  revision,
  tags,
  columnNames,
  onError,
}: {
  project: ProjectDetail
  taskId: string
  revision: number
  tags: TaskTag[]
  columnNames: Record<string, string>
  onError: (cause: unknown) => void
}) {
  const stateRevision = useProjectRevision('tasks')
  const [kind, setKind] = useState('all')
  const [page, setPage] = useState<TaskActivityPage | null>(null)
  const readScope = `${stateRevision ?? ''}:${revision}`
  const [position, setPosition] = useState({ cursor: '', scope: readScope })
  const cursor = position.scope === readScope ? position.cursor : ''
  const setCursor = (next: string) => setPosition({ cursor: next, scope: readScope })
  const [retry, setRetry] = useState(0)
  const [error, setError] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    getTaskActivity(project.id, taskId, kind, cursor, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) {
          setPage((previous) =>
            cursor && previous
              ? { ...result, items: [...previous.items, ...result.items] }
              : result,
          )
          setError('')
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setPage(null)
          setError(cause instanceof Error ? cause.message : '动态读取失败。')
          onError(cause)
        }
      })
    return () => controller.abort()
  }, [project.id, taskId, revision, kind, cursor, retry, onError, stateRevision])
  function value(key: string, raw: unknown): string {
    if (raw === null || raw === undefined || raw === '') return '未设置'
    if (Array.isArray(raw))
      return raw.map((item) => value(key, item)).join('、') || '未设置'
    const text = String(raw)
    if (key === 'owner_member_id' || key === 'participant_ids')
      return (
        project.members.find((member) => member.id === text)?.display_name ??
        text
      )
    if (key === 'tag_ids')
      return tags.find((tag) => tag.id === text)?.name ?? text
    if (key === 'column_id') return columnNames[text] ?? text
    if (key === 'status') return states[text] ?? text
    if (key.endsWith('_at'))
      return new Date(text).toLocaleString('zh-CN', {
        timeZone: project.business_timezone || 'UTC',
        hour12: false,
      })
    return text
  }
  return (
    <section className="task-inspector-activity" aria-label="任务动态">
      <header>
        <h3>动态</h3>
        <div role="group" aria-label="筛选任务动态">
          {[
            ['all', '全部'],
            ['change', '修改记录'],
            ['comment', '评论'],
          ].map(([key, name]) => (
            <Button
              key={key}
              variant="ghost"
              aria-pressed={kind === key}
              onClick={() => {
                setKind(key)
                setCursor('')
                setPage(null)
              }}
            >
              {name}
            </Button>
          ))}
        </div>
      </header>
      {error && (
        <p role="alert">
          {error}
          <Button
            onClick={() => {
              setCursor('')
              setRetry((n) => n + 1)
            }}
          >
            重新读取动态
          </Button>
        </p>
      )}
      {!page && !error && <p role="status">正在读取动态…</p>}
      {page && (
        <>
          <ul className="task-timeline">
            {page.items.map((event) => (
              <li key={event.id} data-kind={event.kind}>
                <header>
                  <strong title={event.actor_id}>
                    {event.actor_name || event.actor_id}
                  </strong>
                  <span>
                    {event.kind === 'comment'
                      ? '发表评论'
                      : event.action === 'task.created'
                        ? '创建任务'
                        : '修改任务'}
                  </span>
                  <time dateTime={event.created_at}>
                    {new Date(event.created_at).toLocaleString('zh-CN', {
                      timeZone: project.business_timezone || 'UTC',
                      hour12: false,
                    })}
                  </time>
                </header>
                {event.kind === 'comment' ? (
                  <p>{event.body}</p>
                ) : (
                  <dl>
                    {Object.keys(event.after).map((key) => (
                      <div key={key}>
                        <dt>{labels[key] ?? key}</dt>
                        <dd>
                          <del>{value(key, event.before[key])}</del>
                          <span aria-label="改为"> → </span>
                          <span>{value(key, event.after[key])}</span>
                        </dd>
                      </div>
                    ))}
                  </dl>
                )}
              </li>
            ))}
          </ul>
          {!page.items.length && <p>暂无此类动态。</p>}
          {page.next_cursor && (
            <Button
              variant="outline"
              onClick={() => setCursor(page.next_cursor!)}
            >
              加载更多动态
            </Button>
          )}
        </>
      )}
    </section>
  )
}
