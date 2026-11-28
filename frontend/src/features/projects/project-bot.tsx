import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { z } from '../../api/validation.ts'
import { ArrowUp, Plus } from 'lucide-react'
import { MarkdownText, UserMessageBubble, FishLogo } from '@deepseek-ai/dsh-client-ui-primitives'
import composer from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/InputBar.module.css'
import heroStyle from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/HeroShell.module.css'
import { errorStatus } from '../../api/session-client.ts'
import { client } from '../../api/session'
import { type ProjectRequest } from './project-contracts'
import { briefReceipts, BriefReceiptLinks } from './BriefReceiptLinks'
import { AgentConfig } from '../agents/AgentPages'
import './project-bot.css'

const prompts = [
  ['梳理项目进展', '帮我梳理一下当前项目的进展。'],
  ['查看进行中任务', '哪些任务还在进行中？'],
  ['查询项目成员', '列出项目成员和他们的角色。'],
]
const pending = new Set(['route', 'running', 'ready', 'sending'])
const replyStatus: Record<string, string> = {
  stale: '查询时的来源数据已变化，请重新查询。',
  suppressed: '当前无法显示这条回复。',
  expired: '项目 PM 回复超时，可以重新提问。',
  unknown: '暂未确认回复结果。',
}
const markdownLabels = {
  code: {
    copyLabel: '复制代码',
    copiedLabel: '已复制',
    toolbarLabels: { codeLabel: '代码', wrapLabel: '自动换行', unwrapLabel: '关闭换行' },
  },
  footnotes: '脚注',
}
const messagesSchema = z.object({
  items: z.array(
    z.object({
      id: z.string(),
      question: z.string(),
      answer: z.string().nullish(),
      state: z.string(),
      briefs: briefReceipts.optional(),
      primary_saved: z.boolean().optional(),
      agent_name: z.string().optional(),
    }),
  ),
})
type ConversationProps = {
  project: string
  base: string
  request: ProjectRequest
  signal: AbortSignal
  settings: boolean
}
type Draft = { text: string; intent: string; retry?: { text: string; intent: string; key: string } }

function ProjectConversation({
  project,
  request,
  signal,
  thread,
  stored,
  update,
  lateAck,
  onLateAck,
}: ConversationProps & {
  thread: string
  stored: Draft
  update: (d: Draft) => void
  lateAck?: string
  onLateAck: (key: string) => void
}) {
  const [items, setItems] = useState<z.infer<typeof messagesSchema>['items']>([])
  const [openState, setOpenState] = useState('loading')
  const [notice, setNotice] = useState('正在读取对话…')
  const [sending, setSending] = useState(false)
  const [draft, setText] = useState(stored.text)
  const [intent, setIntent] = useState(stored.intent)
  const setDraft = (text: string) => {
    setText(text)
    if (retry.current?.text !== text.trim()) retry.current = undefined
    update({ text, intent, retry: retry.current })
  }
  const [questionsOpen, setQuestionsOpen] = useState(false)
  const transcript = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const retry = useRef<Draft['retry']>(stored.retry)
  const mounted = useRef(false)
  const inFlight = useRef(false)
  const editor = useRef<HTMLDivElement>(null)
  const refreshRef = useRef<() => Promise<void>>(async () => {})
  const url =
    '/api/v1/projects/' +
    encodeURIComponent(project) +
    '/bot/messages' +
    (thread ? '?conversation=' + encodeURIComponent(thread) : '')
  useLayoutEffect(() => {
    mounted.current = true
    if (editor.current) editor.current.textContent = stored.text
    return () => {
      mounted.current = false
    }
  }, [])
  const running = items.some((message) => pending.has(message.state))
  const length = [...draft.trim()].length
  const tooLong = length > 2000
  useEffect(() => {
    let timer: number | undefined
    let revision = 0
    async function refresh() {
      const attempt = ++revision
      clearTimeout(timer)
      try {
        const response = messagesSchema.parse(await request(url, { signal, activity: 'poll' }))
        if (signal.aborted || attempt !== revision) return
        setItems(response.items)
        setOpenState('open')
        setNotice('')
        if (response.items.some((message) => pending.has(message.state)))
          timer = window.setTimeout(refresh, 1200)
      } catch (error) {
        if (signal.aborted || attempt !== revision) return
        setOpenState('error')
        if ([401, 403, 404].includes(errorStatus(error))) {
          setItems([])
          retry.current = undefined
          setText('')
          update({ text: '', intent: '' })
          editor.current?.replaceChildren()
          setNotice(errorStatus(error) === 401 ? '请重新登录以读取对话。' : '当前项目对话不可访问。')
        } else {
          setNotice('暂时无法读取对话，正在重试…')
          timer = window.setTimeout(refresh, 5000)
        }
      }
    }
    refreshRef.current = refresh
    void refresh()
    return () => {
      revision++
      clearTimeout(timer)
    }
  }, [url, request, signal])
  useEffect(() => {
    if (!lateAck) return
    if (retry.current?.key === lateAck) {
      retry.current = undefined
      editor.current?.replaceChildren()
      setText('')
    }
    void refreshRef.current()
  }, [lateAck])

  async function submit() {
    const text = editor.current?.innerText.trim() ?? ''
    if (
      signal.aborted ||
      inFlight.current ||
      openState !== 'open' ||
      running ||
      !text ||
      [...text].length > 2000
    )
      return
    inFlight.current = true
    setSending(true)
    setQuestionsOpen(false)
    setNotice('正在发送…')
    if (retry.current?.text !== text || retry.current.intent !== intent)
      retry.current = { text, intent, key: crypto.randomUUID() }
    update({ text, intent, retry: retry.current })
    try {
      const key = retry.current.key
      await request(url, {
        method: 'POST',
        body: intent ? { text, intent } : { text },
        idempotencyKey: key,
        signal,
      })
      if (signal.aborted) return
      retry.current = undefined
      if (!mounted.current) {
        onLateAck(key)
        return
      }
      follow.current = true
      if (editor.current?.innerText.trim() === text) {
        editor.current.replaceChildren()
        setDraft('')
      }
      await refreshRef.current()
    } catch (error) {
      if (signal.aborted || !mounted.current) return
      if ([401, 403, 404, 409].includes(errorStatus(error))) await refreshRef.current()
      else setNotice('发送未确认，可以重试。')
    } finally {
      inFlight.current = false
      if (!signal.aborted && mounted.current) setSending(false)
    }
  }

  useLayoutEffect(() => {
    const el = transcript.current
    if (follow.current && el) el.scrollTop = el.scrollHeight
  }, [items])

  const blank = !items.length
  const hero = blank && openState === 'open'
  const waitingFor = items.find((item) => pending.has(item.state))?.agent_name ?? '项目 PM'
  const recipient =
    intent === 'knowledge' ? '知识问答 Agent' : intent === 'admin' ? '全局管理 Agent' : '项目 PM'
  const messageStatus = tooLong
    ? '每条消息最多 2000 字。'
    : notice || (running ? waitingFor + ' 正在回复…' : '')
  const customDraft = draft.trim() !== '' && !prompts.some(([, question]) => question === draft)
  const suggestions = (
    <div
      className="dsh-questions"
      role={questionsOpen ? 'listbox' : 'group'}
      aria-label="常用问题"
      onKeyDown={(event) => {
        if (!questionsOpen) return
        if (event.key === 'Escape') {
          event.preventDefault()
          setQuestionsOpen(false)
          editor.current?.focus()
        } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault()
          const options = [...event.currentTarget.querySelectorAll('button')]
          const next =
            options.findIndex((option) => option === event.target) + (event.key === 'ArrowDown' ? 1 : -1)
          options[(next + options.length) % options.length].focus()
        }
      }}
    >
      {prompts.map(([label, question], index) => (
        <button
          key={label}
          type="button"
          role={questionsOpen ? 'option' : undefined}
          autoFocus={questionsOpen && index === 0}
          aria-selected={questionsOpen ? draft === question : undefined}
          disabled={openState !== 'open' || sending || running || customDraft}
          onClick={() => {
            if (editor.current) editor.current.textContent = question
            setDraft(question)
            editor.current?.focus()
            setQuestionsOpen(false)
          }}
        >
          {label}
        </button>
      ))}
    </div>
  )

  return (
    <>
      <header className="dsh-project-head">
        <label>
          执行角色{' '}
          <select
            value={intent}
            disabled={sending}
            onChange={(e) => {
              setIntent(e.target.value)
              update({ text: draft, intent: e.target.value })
            }}
          >
            <option value="">按请求选择</option>
            <option value="project">项目 PM</option>
            <option value="knowledge">知识问答</option>
            {client.session?.access?.navigation?.agents && <option value="admin">全局管理</option>}
          </select>
        </label>
      </header>
      <div className="dsh-project-body" data-hero={hero}>
        <div
          className="dsh-history"
          ref={transcript}
          data-conversation-scroll
          onScroll={(e) => {
            const el = e.currentTarget
            follow.current = el.scrollHeight - el.clientHeight - el.scrollTop < 32
          }}
        >
          {items.length > 0 && (
            <ol aria-label="我的项目对话">
              {items.map((message) => {
                const split = message.answer?.indexOf('\n') ?? -1
                const author = split > 0 ? message.answer?.slice(0, split) : (message.agent_name ?? '项目 PM')
                const answer = split > 0 ? message.answer?.slice(split + 1) : message.answer
                return (
                  <li key={message.id}>
                    <UserMessageBubble className="dsh-question" aria-label="我">
                      {message.question}
                    </UserMessageBubble>
                    {(answer || !pending.has(message.state)) && (
                      <article>
                        <strong>{author}</strong>
                        {answer ? (
                          <MarkdownText text={answer} labels={markdownLabels} omitHtml />
                        ) : (
                          <p>
                            {message.primary_saved
                              ? '这次主群设置已保存。'
                              : (replyStatus[message.state] ?? '这条消息未能完成。')}
                          </p>
                        )}
                        {message.briefs && <BriefReceiptLinks project={project} receipts={message.briefs} />}
                      </article>
                    )}
                  </li>
                )
              })}
            </ol>
          )}
        </div>
        <div className="dsh-compose">
          {hero && (
            <div className="dsh-hero">
              <div className={heroStyle.root}>
                <div className={heroStyle.headline}>
                  <FishLogo className={heroStyle.fish} size={34} />
                  <span className={heroStyle.titleGroup}>
                    探索未至之境<span className={heroStyle.previewBadge}>预览版</span>
                  </span>
                </div>
              </div>
            </div>
          )}
          <div role="status" className="dsh-notice">
            {messageStatus}
          </div>
          <div className={[composer.root, hero && composer.hero].filter(Boolean).join(' ')}>
            <div className={composer.card} data-composer-card>
              <div className={composer.overlayAnchor} data-composer-overlay>
                {questionsOpen && suggestions}
              </div>
              <div className={composer.scroll}>
                <div className={composer.grow}>
                  <div
                    ref={editor}
                    className={composer.input}
                    data-composer-input
                    role="textbox"
                    aria-multiline="true"
                    aria-label={'给' + recipient + ' 发消息…'}
                    contentEditable={openState === 'open' ? 'plaintext-only' : false}
                    aria-disabled={openState !== 'open'}
                    onInput={(event) => setDraft(event.currentTarget.innerText)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                        event.preventDefault()
                        void submit()
                      }
                      if (event.key === 'Escape') setQuestionsOpen(false)
                    }}
                  />
                  {!draft && (
                    <span aria-hidden="true" className={composer.placeholder}>
                      {'给' + recipient + ' 发消息…'}
                    </span>
                  )}
                </div>
              </div>
              <div className={composer.row}>
                <div className={composer.tools}>
                  <button
                    type="button"
                    className={composer.add}
                    aria-label="常用问题"
                    aria-haspopup="listbox"
                    aria-expanded={questionsOpen}
                    disabled={customDraft || running || openState !== 'open'}
                    onClick={() => setQuestionsOpen((value) => !value)}
                  >
                    <Plus size={14} />
                  </button>
                </div>
                <div className={composer.trailing}>
                  <button
                    type="button"
                    className={composer.primary}
                    aria-label="发送消息"
                    disabled={openState !== 'open' || sending || running || !length || tooLong}
                    onClick={() => void submit()}
                  >
                    <ArrowUp size={18} />
                  </button>
                </div>
              </div>
            </div>
          </div>
          {hero && !questionsOpen && suggestions}
        </div>
      </div>
    </>
  )
}

export function ProjectBot(props: ConversationProps) {
  const { project, base, request, signal, settings } = props
  const [sessions, setSessions] = useState<{ id: string; title: string }[]>([])
  const [thread, setThread] = useState('')
  const [title, setTitle] = useState(''),
    [creating, setCreating] = useState(false),
    [busy, setBusy] = useState(false),
    [problem, setProblem] = useState('')
  const [revision, reload] = useState(0)
  const createKey = useRef<{ title: string; key: string } | undefined>(undefined)
  const drafts = useRef(new Map<string, Draft>())
  const [lastAck, setLastAck] = useState<{ thread: string; key: string }>()
  const url = '/api/v1/projects/' + encodeURIComponent(project) + '/bot/conversations'
  useEffect(() => {
    if (settings) return
    const controller = new AbortController()
    request(url, { signal: AbortSignal.any([signal, controller.signal]), activity: 'poll' })
      .then((raw) => {
        if (!controller.signal.aborted && !signal.aborted) {
          setSessions(
            z.object({ items: z.array(z.object({ id: z.string(), title: z.string() })) }).parse(raw).items,
          )
          setProblem('')
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted && !signal.aborted) {
          setProblem('无法读取会话列表。')
          if ([401, 403].includes(errorStatus(e))) {
            setSessions([])
            drafts.current.clear()
            setThread('')
          }
        }
      })
    return () => controller.abort()
  }, [project, request, signal, url, revision, settings])
  async function newConversation() {
    if (busy || !title.trim()) return
    setBusy(true)
    if (createKey.current?.title !== title.trim())
      createKey.current = { title: title.trim(), key: crypto.randomUUID() }
    try {
      const item = z.object({ id: z.string(), title: z.string() }).parse(
        await request(url, {
          method: 'POST',
          signal,
          idempotencyKey: createKey.current.key,
          body: { title: title.trim() },
        }),
      )
      if (signal.aborted) return
      setSessions((old) => [...old.filter((s) => s.id !== item.id), item])
      setThread(item.id)
      setTitle('')
      setCreating(false)
      createKey.current = undefined
      setProblem('')
    } catch {
      if (!signal.aborted) setProblem('会话创建未确认，请重试。')
    } finally {
      if (!signal.aborted) setBusy(false)
    }
  }
  useLayoutEffect(() => {
    const syncTheme = () =>
      document.body.toggleAttribute('data-ds-dark-theme', document.documentElement.dataset.theme === 'dark')
    syncTheme()
    const observer = new MutationObserver(syncTheme)
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => {
      observer.disconnect()
      document.body.removeAttribute('data-ds-dark-theme')
    }
  }, [])
  if (settings)
    return (
      <section>
        <header className="dsh-project-head">
          <h2>Bot 设置</h2>
          <a href={base + '/bot'}>返回对话</a>
          <a href={base + '/bot?view=connection'}>飞书接入</a>
        </header>
        <AgentConfig
          title="项目 Agent 配置"
          request={request}
          url={'/api/v1/projects/' + encodeURIComponent(project) + '/agents/configurations/project-pm'}
        />
      </section>
    )
  return (
    <section className="dsh-project-chat" aria-label="项目 PM 对话">
      <header className="dsh-project-head">
        <h2>项目 PM 对话</h2>
        <div className="agent-actions">
          <button className="button" type="button" onClick={() => setCreating((v) => !v)}>
            新建会话
          </button>
          <a href={base + '/bot?view=settings'}>Bot 设置</a>
          <a href={base + '/bot?view=connection'}>飞书接入</a>
        </div>
      </header>
      {creating && (
        <form
          className="agent-actions"
          onSubmit={(e) => {
            e.preventDefault()
            void newConversation()
          }}
        >
          <label>
            会话名称{' '}
            <input
              autoFocus
              required
              maxLength={80}
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </label>
          <button className="button" type="submit" disabled={!title.trim() || busy}>
            创建
          </button>
        </form>
      )}
      {problem && (
        <p role="alert">
          {problem}
          <button className="button" type="button" onClick={() => reload((i) => i + 1)}>
            重新读取
          </button>
        </p>
      )}
      <div className="agent-chat-layout">
        <aside aria-label="我的会话">
          <h3>我的会话</h3>
          {sessions.map((s) => (
            <button
              key={s.id}
              className="button"
              type="button"
              aria-pressed={thread === s.id}
              onClick={() => setThread(s.id)}
            >
              {s.title}
            </button>
          ))}
        </aside>
        <div className="agent-chat-room">
          <ProjectConversation
            key={thread}
            {...props}
            thread={thread}
            stored={drafts.current.get(thread) ?? { text: '', intent: '' }}
            update={(d) => {
              drafts.current.set(thread, d)
            }}
            lateAck={lastAck?.thread === thread ? lastAck.key : undefined}
            onLateAck={(key) => {
              const old = drafts.current.get(thread)
              if (old?.retry?.key === key) drafts.current.set(thread, { text: '', intent: old.intent })
              setLastAck({ thread, key })
            }}
          />
        </div>
      </div>
    </section>
  )
}
