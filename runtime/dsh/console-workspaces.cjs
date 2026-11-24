// Register existing Agent sessions with the native Web workspace sidebar.
const path = require('node:path');
exports.name = 'omnirelay-console-workspaces';
exports.inject = ['workspaceRegistry', 'sessionPersistence', 'commands', 'agents'];
exports.apply = async ctx => {
  const root = process.env.OMNI_AGENT_DIRECTORY;
  const registered = new Set();
  async function refresh() {
    for (const {header} of await ctx.sessionPersistence.list()) {
      if (!header.cwd || registered.has(header.id)) continue;
      const agent = path.basename(path.dirname(header.cwd));
      if (!/^[0-9a-f]{64}$/.test(agent) || header.cwd !== path.join(root, agent, 'workspace')) continue;
      const workspace = await ctx.workspaceRegistry.create(header.cwd, 'Agent ' + agent.slice(0,12));
      if (!workspace.sessionIds.includes(header.id)) await workspace.attachSession(header.id);
      registered.add(header.id);
    }
  }
  const handles = new WeakMap();
  ctx.provide('consoleAgentOwner', {agents: ctx.agents, handles});
  ctx.commands.register({
    definitionId: 'omnirelay-release-session', name: 'handoff',
    description: '保存本会话并将写入权交还项目 Bot',
    async handler({agent, rawInput}) {
      if (rawInput.trim() || agent.status !== 'idle')
        return {kind: 'error', text: '请等待当前轮次结束后执行 /handoff。'};
      await ctx.sessionPersistence.flush();
      if (agent.status !== 'idle') return {kind: 'error', text: '当前会话正在运行。'};
      const handle = handles.get(agent);
      if (!handle) return {kind: 'error', text: '此会话的写入权由其他 DSH 任务持有。'};
      // DSH appends command/done after the handler returns. Close the owning
      // handle after that append, so the command result is durable too.
      setImmediate(async () => {
        try {
          await ctx.sessionPersistence.flush();
          if (agent.status === 'idle' && ctx.agents.get(agent.id) === agent) {
            await handle.dispose();
            handles.delete(agent);
          }
        } catch {console.error('Explicit session handoff failed');}
      });
      return {kind: 'success', text: '已请求交还写入权。当前命令保存后，空闲会话将交还项目 Bot。'};
    }
  });
  await refresh();
  console.log('OmniRelay native workspaces ready');
  let active = true;
  let timer;
  async function poll() {
    try {await refresh();} catch {console.error('Native workspace refresh failed');}
    if (active) timer = setTimeout(poll, 15000);
  }
  timer = setTimeout(poll, 15000);
  ctx.effect(() => () => {active = false; clearTimeout(timer);});
};
