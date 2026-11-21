const http = require('node:http');

exports.name = 'omnirelay-context';
exports.inject = ['tools'];
exports.apply = (ctx) => {
  ctx.tools.register({
    name: 'context_whoami',
    description: 'Read the authenticated current user and project. No arguments.',
    parameters: {type: 'object', properties: {}, additionalProperties: false},
    output: {
      schema: {type: 'object', properties: Object.fromEntries(
        ['user_id', 'user_name', 'project_id', 'project_name'].map(key => [key, {type: 'string'}])
      ), required: ['user_id', 'user_name', 'project_id', 'project_name'], additionalProperties: false},
      render: (_, value) => [{type: 'text', text: JSON.stringify(value)}],
    },
    async execute(args, exec) {
      if (!exec.agent || !args || Object.keys(args).length !== 0) throw Error('Invalid invocation');
      // Only this plugin can reach the turn-scoped bridge. No model-supplied selectors.
      return new Promise((resolve, reject) => {
        const req = http.request(process.env.OMNI_TOOL_URL, {
          method: 'POST', signal: exec.signal,
          headers: {Authorization: 'Bearer ' + process.env.OMNI_TOOL_NONCE},
        }, (res) => {
          let body = '';
          res.setEncoding('utf8').on('data', (part) => {body += part;});
          res.on('error', reject).on('end', () => {
            try {
              if (res.statusCode !== 200) throw Error('Access unavailable');
              resolve(JSON.parse(body));
            } catch (err) {reject(err);}
          });
        });
        req.on('error', reject);
        req.end(JSON.stringify({session: exec.agent.id}));
      });
    },
  });
};
