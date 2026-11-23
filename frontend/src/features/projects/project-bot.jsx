import {useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import {ConversationRoot} from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/ConversationRoot.tsx';
import {HeroShell} from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/EmptyHero.tsx';
import {InputBar} from '@deepseek-ai/dsh-client-ui-conversation/src/client/skeleton/InputBar.tsx';
import {DraftEditorRuntime} from '@deepseek-ai/dsh-client-ui-conversation/src/client/input/editor/runtime.ts';
import {zh} from '@deepseek-ai/dsh-client-ui-conversation/src/client/locales.ts';
import {MarkdownText, UserMessageBubble} from '@deepseek-ai/dsh-client-ui-primitives';
import {installIconSprite} from '@deepseek-ai/dsh-client-ui-theme/src/client/icon-sprite.ts';
import './project-bot.css';

const prompts = [
  ['梳理项目进展', '帮我梳理一下当前项目的进展。'],
  ['查看进行中任务', '哪些任务还在进行中？'],
  ['查询项目成员', '列出项目成员和他们的角色。'],
];
const pending = new Set(['route', 'running', 'ready', 'sending']);
const replyStatus = {
  stale: '查询时的来源数据已变化，请重新查询。',
  suppressed: '当前无法显示这条回复。',
  expired: '项目 PM 回复超时，可以重新提问。',
  unknown: '暂未确认回复结果。',
};
const markdownLabels = {
  code: {copyLabel: '复制代码', copiedLabel: '已复制', toolbarLabels: {codeLabel: '代码', wrapLabel: '自动换行', unwrapLabel: '关闭换行'}},
  footnotes: '脚注',
};
const t = key => zh[key] ?? key;
const read = value => select => select(value);
const emptyMap = new Map();
const emptyConversation = {activeTargets: new Set()};
const noop = () => {};
const noFileIntake = () => noop;

// The workbench provides one authorized text channel. The DSH input and its
// editor stay mounted when that channel moves from an empty to an active turn.
function ProjectConversation({project, base, request, signal}) {
  const [items, setItems] = useState([]);
  const [openState, setOpenState] = useState('loading');
  const [notice, setNotice] = useState('正在读取对话…');
  const [sending, setSending] = useState(false);
  const [draft, setDraft] = useState('');
  const [questionsOpen, setQuestionsOpen] = useState(false);
  const transcript = useRef(null);
  const follow = useRef(true);
  const retry = useRef(null);
  const inFlight = useRef(false);
  const submitRef = useRef(null);
  const refreshRef = useRef(null);
  const url = '/api/v1/projects/' + encodeURIComponent(project) + '/bot/messages';
  const running = items.some(message => pending.has(message.state));
  const length = [...draft.trim()].length;
  const tooLong = length > 2000;
  const editor = useMemo(() => {
    const runtime = new DraftEditorRuntime({
      lexicon: () => emptyMap, resolveLexicon: () => undefined,
      quoteLabels: () => ({remove: zh['quote.remove']}), openReference: () => false,
      onUpdate: () => {
        runtime.refreshProjection();
        setDraft(runtime.projection.clipboardText);
      },
    });
    return runtime;
  }, []);
  useLayoutEffect(() => editor.register(), [editor]);
  const keyboard = useMemo(() => ({
    editor: editor.editor, paste: text => editor.paste(text), caretSpan: () => editor.caretSpan(),
    submit: () => submitRef.current(), space: () => false, bindFilePicker: noFileIntake,
    dismissPopup: () => setQuestionsOpen(false), arbitrate: () => 'pass',
  }), [editor]);

  useEffect(() => {
    let timer;
    async function refresh() {
      clearTimeout(timer);
      try {
        const response = await request(url, {signal, activity: 'poll'});
        if (signal.aborted) return;
        setItems(response.items);
        setOpenState('open');
        setNotice('');
        if (response.items.some(message => pending.has(message.state))) timer = setTimeout(refresh, 1200);
      } catch (error) {
        if (signal.aborted) return;
        setOpenState('error');
        if ([401, 403, 404].includes(error.status)) {
          setItems([]);
          setNotice(error.status === 401 ? '请重新登录以读取对话。' : '当前项目对话不可访问。');
        } else {
          setNotice('暂时无法读取对话，正在重试…');
          timer = setTimeout(refresh, 5000);
        }
      }
    }
    refreshRef.current = refresh;
    void refresh();
    return () => clearTimeout(timer);
  }, [url, request, signal]);

  async function submit() {
    const text = editor.projection.clipboardText.trim();
    if (signal.aborted || inFlight.current || openState !== 'open' || running || !text || [...text].length > 2000) return;
    inFlight.current = true;
    setSending(true);
    setQuestionsOpen(false);
    setNotice('正在发送…');
    if (retry.current?.text !== text) retry.current = {text, key: crypto.randomUUID()};
    try {
      await request(url, {method: 'POST', body: {text}, idempotencyKey: retry.current.key, signal});
      if (signal.aborted) return;
      retry.current = null;
      follow.current = true;
      editor.clearCommittedDraft(value => value.trim() === text ? value.length : null);
      await refreshRef.current();
    } catch (error) {
      if (signal.aborted) return;
      if ([401, 403, 404, 409].includes(error.status)) await refreshRef.current();
      else setNotice('发送未确认，可以重试。');
    } finally {
      inFlight.current = false;
      if (!signal.aborted) setSending(false);
    }
  }
  submitRef.current = submit;
  useLayoutEffect(() => {
    const el = transcript.current;
    if (follow.current && el) el.scrollTop = el.scrollHeight;
  }, [items]);

  const blank = !items.length;
  const session = {blank, running, openState, pendingSubmissions: [], subagent: null};
  const useSession = read(session);
  const messageStatus = tooLong ? '每条消息最多 2000 字。' : notice || (running ? '项目 PM 正在回复…' : '');
  const customDraft = draft.trim() !== '' && !prompts.some(([, question]) => question === draft);
  const suggestions = <div className="dsh-questions" role={questionsOpen ? 'listbox' : 'group'} aria-label="常用问题"
    onKeyDown={event => {
      if (!questionsOpen) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        setQuestionsOpen(false);
        editor.editor.focus();
      } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        const options = [...event.currentTarget.querySelectorAll('button')];
        const next = options.indexOf(event.target) + (event.key === 'ArrowDown' ? 1 : -1);
        options[(next + options.length) % options.length].focus();
      }
    }}>
    {prompts.map(([label, question], index) => <button key={label} type="button"
      role={questionsOpen ? 'option' : undefined} autoFocus={questionsOpen && index === 0}
      aria-selected={questionsOpen ? draft === question : undefined}
      disabled={openState !== 'open' || sending || running || customDraft}
      onClick={() => { editor.setDraft(question); editor.editor.focus(); setQuestionsOpen(false); }}>{label}</button>)}
  </div>;

  return <ConversationRoot sessionId={project} useSession={useSession}
    useSessions={read({byId: {[project]: {blank: openState === 'open' && blank}}})}
    useConversation={read(emptyConversation)}
    renderSlot={() => <header className="dsh-project-head"><h2>项目 PM 对话</h2><a href={base + '/bot?view=connection'}>飞书接入</a></header>}
    renderFactorySlot={(_slot, {hero}) => <div className="dsh-project-body" data-hero={hero}>
      <div className="dsh-history" ref={transcript} data-conversation-scroll
        onScroll={e => { const el = e.currentTarget; follow.current = el.scrollHeight - el.clientHeight - el.scrollTop < 32; }}>
        {items.length > 0 && <ol aria-label="我的项目对话">{items.map(message => {
          const split = message.answer?.indexOf('\n') ?? -1;
          const author = split > 0 ? message.answer.slice(0, split) : '项目 PM';
          const answer = split > 0 ? message.answer.slice(split + 1) : message.answer;
          return <li key={message.id}>
            <UserMessageBubble className="dsh-question" aria-label="我">{message.question}</UserMessageBubble>
            {(answer || !pending.has(message.state)) && <article><strong>{author}</strong>
              {answer ? <MarkdownText text={answer} labels={markdownLabels} omitHtml/> :
                <p>{message.primary_saved ? '这次主群设置已保存。' : replyStatus[message.state] ?? '这条消息未能完成。'}</p>}
            </article>}
          </li>;
        })}</ol>}
      </div>
      <div className="dsh-compose">
        {hero && <div className="dsh-hero"><HeroShell t={t} renderSlot={(_name, _props, {fallback}) => fallback}/></div>}
        <div role="status" className="dsh-notice">{messageStatus}</div>
        <InputBar sessionId={project} occurrence="embedded" variant={hero ? 'hero' : 'composer'}
          placeholder="给项目 PM 发消息…" disabled={openState !== 'open'}
          useSession={useSession} useInput={read({draft, phase: sending ? 'sending' : 'plain', attachmentIds: [], queue: []})}
          inputActions={{}} keyboard={keyboard} registerFileIntake={noFileIntake}
          useAccess={action => action === 'session/prompt' && !running && !tooLong}
          useBusyEnter={read('queue')} useFileUploads={read({})} useNotices={read(null)} useLexicon={read(emptyMap)}
          useMenuLauncher={read(questionsOpen ? 'command' : null)} useStopShortcut={read([])}
          useProjection={() => undefined} t={key => key === 'input.commands' ? '常用问题' : t(key)}
          toggleCommandMenu={customDraft || running ? undefined : () => setQuestionsOpen(value => !value)}
          renderSlot={name => name === 'conversation.input.overlay' && questionsOpen ? suggestions : null}/>
        {hero && !questionsOpen && suggestions}
      </div>
    </div>}/>;
}

export function ProjectBot(props) {
  useLayoutEffect(() => {
    const syncTheme = () => document.body.toggleAttribute('data-ds-dark-theme', document.documentElement.dataset.theme === 'dark');
    let removeSprite;
    installIconSprite({effect: mount => { removeSprite = mount(); }, on: noop});
    syncTheme();
    const observer = new MutationObserver(syncTheme);
    observer.observe(document.documentElement, {attributes:true,attributeFilter:['data-theme']});
    return () => {
      observer.disconnect();
      removeSprite();
      document.body.removeAttribute('data-ds-dark-theme');
    };
  }, []);
  return <section className="dsh-project-chat" aria-label="项目 PM 对话"><ProjectConversation {...props}/></section>;
}
