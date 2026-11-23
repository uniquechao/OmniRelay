import type { ReactNode, RefObject } from 'react'
import { z } from '../../api/validation.ts'
import type { Guard } from '../workbench/Account'
import { boardAccessSchema, directorySchema, type ProjectRequest } from './project-contracts'
import type { ProjectRoute } from './projects'
import { Component, lazy, Suspense, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { client } from '../../api/session'
import { Loading, Status } from '../../ui/components/Elements'
import { ChatPage } from '../chat/ChatPage'
import { DrivePage } from '../drive/DrivePage'
import { ProjectMembers } from './ProjectMembers'
import { ProjectFollow } from './ProjectListPage'
import { ProjectPanels, TypeBadge } from './ProjectPanels'
import { openProjectProfile } from './project-profile'
import {
  failure,
  loadProjectWorkspace,
  projectRoute,
  projectTabs,
  projectStatuses,
  projectStatusLabel,
} from './projects'
import '../../../../design/prototype/omni-relay-project.css'

const ProjectBot = lazy(() => import('./project-bot').then((m) => ({ default: m.ProjectBot })))
const Board = lazy(() => import('../task-board/mount').then((m) => ({ default: m.ProjectBoard })))
const tones: Record<string, 'progress' | 'success' | 'danger' | 'warning'> = {
  active: 'progress',
  completed: 'success',
  blocked: 'danger',
  paused: 'warning',
}
const tabNames = Object.keys(projectTabs)
const loading = <Loading size="page" label="正在加载…" />

function PanelError({ error, retry }: { error: unknown; retry: () => void }) {
  return (
    <>
      <p className="empty" role="status">
        {failure(error)}
      </p>
      <button type="button" className="button" onClick={retry}>
        重新加载
      </button>
    </>
  )
}
class ProjectPanelBoundary extends Component<{ children: ReactNode; retry: () => void }> {
  state = { error: null }
  static getDerivedStateFromError(error: unknown) {
    return { error }
  }
  render() {
    return this.state.error ? (
      <PanelError error={this.state.error} retry={this.props.retry} />
    ) : (
      this.props.children
    )
  }
}

type Loaded = Awaited<ReturnType<typeof loadProjectWorkspace>>
type Memory = typeof client.scope.projects
function ProjectContent({
  route,
  loaded,
  request,
  signal,
  navigation,
  memory,
  openSettings,
}: {
  route: ProjectRoute
  loaded: Loaded
  request: ProjectRequest
  signal: AbortSignal
  navigation: Guard
  memory: Memory
  openSettings: (trigger: HTMLElement) => void
}) {
  const { id, base, tab, parts } = route
  const { workspace, data: raw, directory, contentRoute, partial } = loaded
  const project = workspace?.project
  if (tab === 'bot' && contentRoute.tab === 'bot')
    return <ProjectBot project={id} {...{ base, request, signal }} />
  if (tab === 'docs')
    return (
      <DrivePage projectId={id} initialQuery={route.params.get('q') || ''} memory={(memory.files ??= {})} />
    )
  if (tab === 'chats') return <ChatPage projectId={id} hash={route.hash} />
  if (tab === 'members')
    return (
      <ProjectMembers
        {...{
          data: directorySchema.parse(raw),
          projectId: id,
          request,
          signal,
          navigation,
          query: route.params.get('q') || '',
        }}
      />
    )
  if (tab === 'tasks' && parts.length <= 1) {
    const data = boardAccessSchema.parse(raw)
    if (parts[0] === 'new' && !data.can_create)
      return (
        <p className="empty" role="status">
          当前账号无权创建任务。
        </p>
      )
    const actions = []
    if (data.can_comment) actions.push('comment')
    if (data.can_create) actions.push('create_task', 'update_own_task')
    if (data.can_configure) actions.push('configure_board')
    return (
      <Board
        project={{
          id,
          name: project?.name || id,
          business_timezone: data.business_timezone,
          current_member_id: data.member_id,
          actions,
          milestones: loaded.milestones.filter((m) => m.kind === 'milestone' && m.id),
          members: directory,
        }}
        manage={data.can_assign}
        navigation={navigation}
        directTask={parts[0]}
        onCloseDirect={() => {
          location.hash = memory.taskViews.get(id) || base + '/tasks'
        }}
      />
    )
  }
  return (
    <>
      {partial && (
        <p className="empty" role="status">
          部分数据暂不可用。
        </p>
      )}
      {contentRoute.tab === 'feishu' && <a href={base + '/bot'}>返回 Bot 对话</a>}
      <ProjectPanels
        route={contentRoute}
        {...{ data: raw, request, signal, openSettings }}
        people={new Map(directory.map((m) => [m.id, m.display_name]))}
      />
    </>
  )
}
function restoreList(memory: Memory, scope: typeof client.scope) {
  const savedKey = history.state?.projectListKey
  const parsed = z
    .object({
      list: z.string(),
      focus: z.string().optional(),
      scroll: z.number().optional(),
      left: z.number().optional(),
      windowY: z.number().optional(),
    })
    .safeParse(savedKey && scope.read('project.' + savedKey))
  const saved = parsed.success ? parsed.data : undefined
  const savedRoute = typeof saved?.list === 'string' && projectRoute(saved.list)
  if (savedRoute && !savedRoute.id) Object.assign(memory, saved)
  if (!memory.list) return
  const key = savedKey || crypto.randomUUID()
  const { list, focus, scroll, left, windowY } = memory
  scope.store('project.' + key, JSON.stringify({ list, focus, scroll, left, windowY }))
  history.replaceState({ ...history.state, projectListKey: key }, '')
}

export function ProjectWorkspacePage({ hash, guard }: { hash: string; guard: RefObject<Guard> }) {
  const route = useMemo(() => {
    const value = projectRoute(hash)
    if (!value?.id) throw new Error('无效的项目地址。')
    return value
  }, [hash])
  const { id, base, tab, parts } = route
  const scope = client.scope,
    memory = scope.projects
  const request = useMemo(() => client.bind(), [scope])
  const [result, setResult] = useState<{ loaded: Loaded; signal: AbortSignal } | null>(null),
    [error, setError] = useState<unknown>(null),
    [revision, setRevision] = useState(0)
  const root = useRef<HTMLElement>(null),
    nav = useRef<HTMLDivElement>(null),
    restoreFocus = useRef<string | null>(null)
  useLayoutEffect(() => {
    restoreList(memory, scope)
    const rememberBoard = () => {
      const current = projectRoute(location.hash)
      if (current?.id === id && current.tab === 'tasks' && !current.parts.length)
        memory.taskViews.set(id, location.hash)
    }
    rememberBoard()
    window.addEventListener('omni-grid:hash-params-change', rememberBoard)
    const selected = nav.current?.querySelector<HTMLElement>('[aria-selected=true]')
    if (selected && nav.current && nav.current.scrollWidth > nav.current.clientWidth)
      nav.current.scrollLeft = Math.max(
        0,
        selected.getBoundingClientRect().right - nav.current.getBoundingClientRect().right,
      )
    if (memory.tabFocus && selected) {
      selected.focus({ preventScroll: true })
      memory.tabFocus = false
    } else root.current?.querySelector<HTMLElement>('#page-heading')?.focus({ preventScroll: true })
    return () => {
      window.removeEventListener('omni-grid:hash-params-change', rememberBoard)
      guard.current = {}
    }
  }, [route, guard, scope, memory])
  useLayoutEffect(() => {
    const controller = new AbortController(),
      { signal } = controller
    loadProjectWorkspace(route, request, signal)
      .then((loaded) => {
        if (!signal.aborted) setResult({ loaded, signal })
      })
      .catch((reason) => {
        if (!signal.aborted) setError(reason)
      })
    return () => controller.abort()
  }, [route, request, revision])
  useLayoutEffect(() => {
    if (!result || !restoreFocus.current) return
    ;(
      root.current?.querySelector<HTMLElement>(restoreFocus.current) ||
      root.current?.querySelector<HTMLElement>('#page-heading')
    )?.focus()
    restoreFocus.current = null
  }, [result])
  const workspace = result?.loaded.workspace,
    project = workspace?.project
  function refresh(selector?: string) {
    restoreFocus.current = selector || '[data-project-settings]'
    setError(null)
    setResult(null)
    setRevision((n) => n + 1)
  }
  function switchTab(next: string) {
    if (next === tab) return
    memory.tabFocus = true
    location.hash = next === 'tasks' ? (memory.taskViews.get(id) ?? base + '/tasks') : base + '/' + next
  }
  function openSettings(trigger: HTMLElement) {
    if (!workspace || !result || !project) return
    openProjectProfile({
      project,
      projectTypes: workspace.project_types,
      trigger,
      statuses: projectStatuses,
      statusLabel: projectStatusLabel,
      request,
      signal: result.signal,
      navigation: guard.current,
      refresh,
    })
  }
  return (
    <main id="page" className="page" data-layout="project" ref={root}>
      <header className="head">
        <div className="project-title">
          {project?.project_type && (
            <TypeBadge type={project.project_type} types={workspace?.project_types ?? []} />
          )}
          <h1 id="page-heading" tabIndex={-1}>
            {project?.name || project?.id || '项目工作台'}
          </h1>
          {project && (
            <>
              <Status tone={tones[project.status] || 'pending'}>{projectStatusLabel(project.status)}</Status>
              <ProjectFollow row={project} />
            </>
          )}
        </div>
        <a className="button" href={memory.list || '#/projects'}>
          返回项目列表
        </a>
      </header>
      <div
        className="tabs"
        role="tablist"
        aria-label="项目工作台"
        ref={nav}
        onKeyDown={(e) => {
          const index = tabNames.indexOf(e.target instanceof HTMLElement ? (e.target.dataset.tab ?? '') : '')
          if (index < 0) return
          const n = tabNames.length,
            moves: Record<string, number> = {
              ArrowRight: (index + 1) % n,
              ArrowLeft: (index + n - 1) % n,
              Home: 0,
              End: n - 1,
            }
          const move = moves[e.key]
          if (move !== undefined) {
            e.preventDefault()
            switchTab(tabNames[move])
          }
        }}
      >
        {tabNames.map((key) => (
          <button
            key={key}
            role="tab"
            id={'tab-' + key}
            aria-selected={key === tab}
            aria-controls="project-panel"
            tabIndex={key === tab ? 0 : -1}
            data-tab={key}
            onClick={() => switchTab(key)}
          >
            {projectTabs[key]}
          </button>
        ))}
      </div>
      <section
        id="project-panel"
        className="stack"
        role="tabpanel"
        aria-labelledby={'tab-' + tab}
        tabIndex={0}
        aria-busy={(!error && !result) || undefined}
      >
        {error ? (
          <PanelError error={error} retry={() => refresh('#page-heading')} />
        ) : result ? (
          <ProjectPanelBoundary retry={() => location.reload()}>
            <Suspense fallback={loading}>
              <ProjectContent
                {...result}
                {...{ route, request, memory, openSettings }}
                navigation={guard.current}
              />
            </Suspense>
          </ProjectPanelBoundary>
        ) : (
          loading
        )}
      </section>
    </main>
  )
}
