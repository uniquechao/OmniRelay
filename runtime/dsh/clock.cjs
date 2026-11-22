exports.name = 'omnirelay-one-shot';
exports.inject = ['schedule','tools'];
exports.apply = (ctx) => {
  ctx.tools.register({
    name: 'activate_notice', description: 'Activate the pre-authorized one-time reminder in this coordinator.',
    parameters: {type:'object', properties:{}, additionalProperties:false},
    output: {schema:{type:'object', properties:{native:{type:'string'}, occurred:{type:'string'}},
      required:['native','occurred'], additionalProperties:false},
      render: (_, value) => [{type:'text',text:JSON.stringify(value)}]},
    async execute(args, exec) {
      const plan = process.env.OMNI_PLAN;
      if (!exec.agent || exec.agent.id !== plan || !args || Object.keys(args).length) throw Error('Unbound plan');
      // This home is exclusive to a single registered plan. Restart resumes its native timer.
      let task = (await ctx.schedule.list()).items.find(item => item.title === plan);
      if (!task) {
        const start = new Date(process.env.OMNI_DUE).toISOString().replace(/[-:]/g,'').replace(/\.\d+Z$/, 'Z');
        task = await ctx.schedule.create({title:plan, prompt:'OMNI_NOTICE_DUE', timing:{kind:'rrule',
          rrule:'DTSTART:'+start+'\nRRULE:FREQ=DAILY;COUNT=1'}, target:{kind:'session',sessionId:plan}},plan,exec.signal);
      }
      const receipt = task.lastDelivery;
      return {native:task.id, occurred:receipt?.targetSessionId === plan ? receipt.scheduledAt : ''};
    }
  });
};
