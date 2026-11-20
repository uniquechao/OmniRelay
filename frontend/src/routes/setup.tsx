import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import { createRoot } from 'react-dom/client';
import { Button, Loading, TextField, ThemeToggle } from '../ui/components';
import logo from '../../../design/prototype/omni-relay-logo-bicolor.png';

const messages: Record<string, string> = {
  setup_forbidden: '设置码无效。请核对设置码，并从产品的 HTTPS 地址打开此页。',
  setup_invalid: '请检查飞书配置字段后重试。',
  setup_unavailable: '暂时无法保存配置。请稍后重试。',
};
const oauthErrors: Record<string, string> = {
  authorization: '本次飞书授权已失效。请在当前浏览器重新发起授权。',
  credentials: '飞书未接受应用凭据。请核对同一应用的 App ID 和 App Secret。',
  denied: '飞书拒绝了当前账号或应用。请检查应用是否已启用，以及账号是否在可用范围内。',
  token_exchange: '飞书授权码换取令牌失败。请重新发起授权，若仍失败请联系部署管理员。',
  user_info: '已收到飞书令牌，但读取用户信息失败。请重试，若仍失败请联系部署管理员。',
  identity: '飞书返回的账号身份不可用。请确认使用了该应用可访问的企业账号。',
  cancelled: '飞书授权已取消。准备好后可以重新提交。',
  unavailable: '暂时无法完成设置。请稍后重试。',
};

function ServiceWait() {
  const [expired, setExpired] = useState(false);
  const retry = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (expired) {
      retry.current?.focus();
      return;
    }
    const lifetime = new AbortController();
    let timer: number;
    async function probe(attempt: number) {
      const response = await fetch('/setup', {
        cache: 'no-store',
        signal: AbortSignal.any([lifetime.signal, AbortSignal.timeout(2000)])
      }).catch(() => null);
      if (lifetime.signal.aborted) return;
      if (response?.status === 404) {
        location.replace('/');
      } else if (attempt === 20) {
        setExpired(true);
      } else {
        timer = window.setTimeout(() => void probe(attempt + 1), 500);
      }
    }
    void probe(1);
    return () => { lifetime.abort(); clearTimeout(timer); };
  }, [expired]);
  return expired ? <>
    <p role="status">配置已保存，登录服务尚未就绪。</p>
    <Button ref={retry} onClick={() => setExpired(false)}>重新检查</Button>
  </> : <Loading size="compact" label="配置已保存，正在启动登录服务…"/>;
}

function SetupForm() {
  const failure = new URLSearchParams(location.search).get('error') ?? '';
  const [error, setError] = useState(Object.hasOwn(oauthErrors, failure) ? oauthErrors[failure] : '');
  const [busy, setBusy] = useState(false);
  const locked = useRef(false);
  const dirty = useRef(false);
  const formRef = useRef<HTMLFormElement>(null);
  const feedback = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => { if (error) feedback.current?.focus(); }, [error]);
  useEffect(() => {
    const confirm = (event: BeforeUnloadEvent) => { if (dirty.current) event.preventDefault(); };
    const clear = () => {
      dirty.current = false;
      formRef.current?.reset();
    };
    window.addEventListener('beforeunload', confirm);
    window.addEventListener('pagehide', clear);
    return () => {
      window.removeEventListener('beforeunload', confirm);
      window.removeEventListener('pagehide', clear);
    };
  }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (locked.current) return;
    locked.current = true;
    setBusy(true);
    setError('');
    const form = event.currentTarget;
    try {
      const response = await fetch('/setup', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, cache: 'no-store',
        body: JSON.stringify(Object.fromEntries(new FormData(form))), signal: AbortSignal.timeout(15000)
      });
      if (response.status === 409) {
        dirty.current = false;
        form.reset();
        location.replace('/?setup=complete');
        return;
      }
      if (response.ok) {
        const destination = new URL((await response.json()).authorization_url);
        if (destination.origin !== 'https://accounts.feishu.cn' || destination.username || destination.password) {
          throw new Error();
        }
        dirty.current = false;
        form.reset();
        location.assign(destination.href);
        return;
      }
      const code = (await response.text()).trim();
      setError(Object.hasOwn(messages, code) ? messages[code] : messages.setup_unavailable);
    } catch {
      setError('暂时无法发起飞书授权。请稍后重试。');
    }
    locked.current = false;
    setBusy(false);
  }

  return <>
    <p className="setup-callback">在飞书开放平台登记重定向 URL <code translate="no">{location.origin}/auth/feishu/callback</code></p>
    <form ref={formRef} className="login-form" id="setup-form" method="post" action="/setup" autoComplete="off"
      onSubmit={submit} onInput={() => { dirty.current = true; }} aria-busy={busy}>
      <div className="field"><label htmlFor="setup-code">一次性设置码</label>
        <input id="setup-code" type="password" name="setup_code" required maxLength={43} readOnly={busy} aria-describedby="setup-code-help"/>
        <small id="setup-code-help">向部署管理员获取设置码。</small></div>
      <TextField id="app-id" label="飞书 App ID" name="app_id" autoCapitalize="none" spellCheck={false}
        required maxLength={256} pattern={'cli_[A-Za-z0-9_\\-]+'} readOnly={busy}/>
      <TextField id="app-secret" label="飞书 App Secret" type="password" name="app_secret" required maxLength={512} readOnly={busy}/>
      <p id="setup-admin-help">使用要设为首位管理员的飞书账号完成授权。</p>
      <Button className="login-button" type="submit" tone="primary" busy={busy} aria-describedby="setup-admin-help">
        {busy ? '正在准备授权…' : '使用飞书授权并保存'}
      </Button>
    </form>
    <p className="login-feedback" ref={feedback} role="status" tabIndex={-1} data-tone="error" hidden={!error}>{error}</p>
  </>;
}

const complete = new URLSearchParams(location.search).get('setup') === 'complete';
window.addEventListener('pageshow', event => {
  if (!event.persisted) return;
  if (complete) location.reload();
  else location.replace('/');
});
createRoot(document.getElementById('root')!).render(
  <main className="login-content setup-page" aria-labelledby="setup-heading">
    <div className="login-top"><ThemeToggle/></div>
    <section className="login-entry">
      <div className="login-logo"><img src={logo} width="195" height="65" alt="OmniRelay"/></div>
      <h1 id="setup-heading">首次配置</h1>
      {complete ? <ServiceWait/> : <SetupForm/>}
    </section>
  </main>
);
