import { createRoot } from 'react-dom/client';
import { Entry } from '../ui/Entry';

const complete = new URLSearchParams(location.search).get('setup') === 'complete';
createRoot(document.getElementById('root')!).render(
  <Entry title="首次配置">
    {complete ? <p role="status">配置已保存。</p> :
      <p>飞书授权回调地址 <code>{location.origin}/auth/feishu/callback</code></p>}
  </Entry>
);
