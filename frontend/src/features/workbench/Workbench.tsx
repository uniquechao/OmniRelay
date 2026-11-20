import { client, useSession } from '../../api/session';
import { Entry } from '../../ui/Entry';

export function Workbench() {
  const { phase } = useSession();
  return <Entry title="工作台">
    <p role="status">{{checking:'正在读取会话…', active:'已登录', anonymous:'尚未登录', unverified:'无法读取会话。', revoked:'工作台访问权限已撤回。'}[phase]}</p>
    {(phase === 'unverified' || phase === 'revoked') && <button onClick={() => void client.verify()}>重试</button>}
  </Entry>;
}
