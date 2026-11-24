// Keep the SDK's lifecycle handle for each Agent the Web session controller opens.
exports.name = 'omnirelay-console-agents';
exports.inject = ['consoleAgentOwner'];
exports.apply = ctx => {
  const {agents, handles} = ctx.consoleAgentOwner;
  async function keep(pending) {
    const handle = await pending;
    handles.set(handle.agent, handle);
    return handle;
  }
  ctx.provide('agents', {
    get: id => agents.get(id),
    list: () => agents.list(),
    turnBoundaryOf: session => agents.turnBoundaryOf(session),
    create: options => keep(agents.create(options)),
    resume: options => keep(agents.resume(options))
  });
};
