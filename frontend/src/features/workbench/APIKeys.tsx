import {useEffect, useRef, useState} from 'react';
import {client} from '../../api/session';
import {Button, TextField} from '../../ui/components/Elements';

type KeyInfo = {id:string; label:string; expires_at:string};
const path = '/api/v1/me/api-keys';

export function APIKeys() {
  const [request] = useState(() => client.bind());
  const [keys, setKeys] = useState<KeyInfo[]>();
  const [label, setLabel] = useState('');
  const [secret, setSecret] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const lock = useRef(false);
  useEffect(() => {
    const controller = new AbortController();
    setError('');
    request(path, {signal:controller.signal}).then(data => setKeys((data as {items:KeyInfo[]}).items))
      .catch(() => {if (!controller.signal.aborted) setError('读取密钥失败，请重试。');});
    return () => controller.abort();
  }, [request, attempt]);

  async function change(id?:string) {
    if (lock.current || !keys || (!id && !label.trim())) return;
    lock.current = true;
    setBusy(true);
    setError('');
    try {
      if (id) {
        await request(path+'/'+id, {method:'DELETE'});
        setKeys(keys.filter(k => k.id!==id));
        setConfirm('');
        setSecret('');
      } else {
        const {id,label:issuedLabel,expires_at,key} = await request(path, {method:'POST', body:{label:label.trim()}}) as KeyInfo & {key:string};
        setKeys([{id,label:issuedLabel,expires_at}, ...keys]);
        setSecret(key);
        setLabel('');
      }
    } catch (e) {
      setError((e as {status?:number}).status===409 ? '已有 10 把有效密钥，请先撤销一把。' : '操作未确认，请刷新列表核对。');
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return <>
    <p>使用密钥以你的身份调用工作台 API。每把密钥有效期为 30 天。</p>
    <TextField label="密钥名称" id="api-key-label" maxLength={80} placeholder="例如：本地调试"
      value={label} onChange={e=>setLabel(e.target.value)}
      onKeyDown={e=>{if(e.key==='Enter'){e.preventDefault();void change();}}}/>
    <Button tone="primary" disabled={!keys || busy || !label.trim()} onClick={()=>void change()}>创建密钥</Button>
    {secret && <div className="field"><label htmlFor="issued-api-key">新密钥（仅显示一次）</label>
      <input id="issued-api-key" value={secret} readOnly autoComplete="off" spellCheck={false} onFocus={e=>e.target.select()}/></div>}
    <h3>有效密钥</h3>
    {!keys && !error && <p role="status">正在读取…</p>}
    {keys?.length===0 && <p>暂无有效密钥。</p>}
    {!!keys?.length && <ul>{keys.map(k=><li key={k.id}><strong>{k.label}</strong>{' · '}
      <time dateTime={k.expires_at}>{new Date(k.expires_at).toLocaleDateString('zh-CN')} 到期</time>{' '}
      {confirm===k.id ? <><Button tone="danger" disabled={busy} onClick={()=>void change(k.id)}>确认撤销</Button>{' '}
        <Button disabled={busy} onClick={()=>setConfirm('')}>取消</Button></> :
        <Button aria-label={'撤销 '+k.label} disabled={busy} onClick={()=>setConfirm(k.id)}>撤销</Button>}</li>)}</ul>}
    {error && <div role="alert">{error} <Button disabled={busy} onClick={()=>{setKeys(undefined);setAttempt(n=>n+1);}}>刷新列表</Button></div>}
  </>;
}
