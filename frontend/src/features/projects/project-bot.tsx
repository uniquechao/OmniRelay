import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { z } from '../../api/validation.ts'
import { ArrowUp, Plus } from 'lucide-react'
import { MarkdownText, UserMessageBubble, FishLogo } from '@deepseek-ai/dsh-client-ui-primitives'
import composer from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/InputBar.module.css'
import heroStyle from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/HeroShell.module.css'
import { errorStatus } from '../../api/session-client.ts'
import { type ProjectRequest } from './project-contracts'
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
      primary_saved: z.boolean().optional(),
    }),
  ),
})
type ConversationProps = { project: string; base: string; request: ProjectRequest; signal: AbortSignal }

function ProjectConversation({ project, base, request, signal }: ConversationProps) {
  const [items, setItems] = useState<z.infer<typeof messagesSchema>['items']>([])
  const [openState, setOpenState] = useState('loading')
  const [notice, setNotice] = useState('正在读取对话…')
  const [sending, setSending] = useState(false)
  const [draft, setDraft] = useState('')
  const [questionsOpen, setQuestionsOpen] = useState(false)
  const transcript = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const retry = useRef<{ text: string; key: string } | null>(null)
  const inFlight = useRef(false)
  const editor = useRef<HTMLDivElement>(null)
  const refreshRef = useRef<() => Promise<void>>(async () => {})
  const url = '/api/v1/projects/' + encodeURIComponent(project) + '/bot/messages'
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
    if (retry.current?.text !== text) retry.current = { text, key: crypto.randomUUID() }
    try {
      await request(url, { method: 'POST', body: { text }, idempotencyKey: retry.current.key, signal })
      if (signal.aborted) return
      retry.current = null
      follow.current = true
      if (editor.current?.innerText.trim() === text) {
        editor.current.replaceChildren()
        setDraft('')
      }
      await refreshRef.current()
    } catch (error) {
      if (signal.aborted) return
      if ([401, 403, 404, 409].includes(errorStatus(error))) await refreshRef.current()
      else setNotice('发送未确认，可以重试。')
    } finally {
      inFlight.current = false
      if (!signal.aborted) setSending(false)
    }
  }

  useLayoutEffect(() => {
    const el = transcript.current
    if (follow.current && el) el.scrollTop = el.scrollHeight
  }, [items])

  const blank = !items.length
  const hero = blank && openState === 'open'
  const messageStatus = tooLong ? '每条消息最多 2000 字。' : notice || (running ? '项目 PM 正在回复…' : '')
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
        <h2>项目 PM 对话</h2>
        <a href={base + '/bot?view=connection'}>飞书接入</a>
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
                const author = split > 0 ? message.answer?.slice(0, split) : '项目 PM'
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
                    aria-label="给项目 PM 发消息…"
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
                      给项目 PM 发消息…
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
  return (
    <section className="dsh-project-chat" aria-label="项目 PM 对话">
      <ProjectConversation {...props} />
    </section>
  )
}
