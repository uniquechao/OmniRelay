// Route the native Web Host to the same JSONL stores and write leases as the Agent SDK.
// This module does not start a listener or grant access to any caller.
const fs = require('node:fs/promises');
const path = require('node:path');

exports.name = 'omnirelay-console-sessions';
exports.apply = async ctx => {
  const root = await fs.realpath(process.env.OMNI_AGENT_DIRECTORY);
  const jsonl = await ctx.loader.internal.import('@deepseek-ai/dsh-session-persistence-jsonl', ctx.baseUrl, {});
  const {SessionPersistenceNotFoundError} = await ctx.loader.internal.import('@deepseek-ai/dsh-session-persistence', ctx.baseUrl, {});
  const stores = new Map();
  let discovery;

  async function discover() {
    const adminRoot = path.join(process.env.DSH_HOME, 'sessions');
    const current = new Set([adminRoot]);
    for (const agent of await fs.readdir(root, {withFileTypes: true})) {
      if (!agent.isDirectory() || !/^[0-9a-f]{64}$/.test(agent.name)) continue;
      const dir = path.join(root, agent.name, 'home', 'sessions');
      try {
        const home = path.dirname(dir);
        if (await fs.realpath(home) !== home) continue;
        try {
          if (await fs.realpath(dir) !== dir || !(await fs.stat(dir)).isDirectory()) continue;
        } catch (err) { if (err.code !== 'ENOENT') throw err; }
      } catch (err) { if (err.code === 'ENOENT') continue; throw err; }
      current.add(dir);
    }
    for (const dir of current) {
      if (stores.has(dir)) continue;
      const scope = ctx.isolate('sessionPersistence', Symbol());
      const fiber = scope.plugin(jsonl, {root: dir, compression: 'none'});
      try {
        await fiber.await();
        const store = scope.get('sessionPersistence');
        if (!store) throw new Error('Native session store unavailable');
        stores.set(dir, {fiber, store});
      } catch (err) { await fiber.dispose(); throw err; }
    }
    for (const [dir, item] of stores) {
      if (!current.has(dir)) { stores.delete(dir); await item.fiber.dispose(); }
    }
    return [...stores.values()].map(item => item.store);
  }

  function activeStores() {
    if (!discovery) discovery = discover().finally(() => { discovery = undefined; });
    return discovery;
  }
  async function locate(id, options) {
    let match;
    for (const store of await activeStores()) {
      const snapshot = await store.stat(id, options);
      if (!snapshot) continue;
      if (match) throw new Error('Ambiguous native session identity');
      match = {store, snapshot};
    }
    return match;
  }
  ctx.provide('sessionPersistence', {
    identity: Symbol('production-agent-sessions'),
    async list(options) {
      const sessions = new Map();
      for (const store of await activeStores()) {
        for (const snapshot of await store.list(options)) {
          if (sessions.has(snapshot.header.id)) throw new Error('Ambiguous native session identity');
          sessions.set(snapshot.header.id, snapshot);
        }
      }
      return [...sessions.values()];
    },
    async stat(id, options) { return (await locate(id, options))?.snapshot; },
    async create(header, options) {
      if (await locate(header.id, options)) throw new Error('Session identity already exists');
      const dir = header.cwd ? path.dirname(header.cwd) : '';
      const agent = path.basename(dir);
      const inWorkspace = /^[0-9a-f]{64}$/.test(agent) && header.cwd === path.join(root, agent, 'workspace');
      const target = inWorkspace ? path.join(dir, 'home', 'sessions') : path.join(process.env.DSH_HOME, 'sessions');
      const entry = stores.get(target);
      if (!entry) throw new Error('Native agent store unavailable');
      return entry.store.create(header, options);
    },
    async flush() { await Promise.all([...stores.values()].map(({store}) => store.flush())); },
    async open(id, access, options) {
      const match = await locate(id, options);
      if (!match) throw new SessionPersistenceNotFoundError(id);
      return match.store.open(id, access, options);
    }
  });
};
