const http = require('node:http');

exports.name = 'omnirelay-context';
exports.inject = ['tools'];
exports.apply = (ctx) => {
  function register(name, description, parameters, schema, project) {
    ctx.tools.register({
      name, description, parameters,
      output: {schema, render: (_, value) => [{type: 'text', text: JSON.stringify(value)}]},
      async execute(args, exec) {
        if (!exec.agent || !args) throw Error('Invalid invocation');
        const call = project ? {session: exec.agent.id, tool: name, args} : {session: exec.agent.id};
        return new Promise((resolve, reject) => {
          const req = http.request(process.env.OMNI_TOOL_URL, {
            method: 'POST', signal: exec.signal,
            headers: {Authorization: 'Bearer ' + process.env.OMNI_TOOL_NONCE},
          }, (res) => {
            let body = '';
            res.setEncoding('utf8').on('data', part => {body += part;});
            res.on('error', reject).on('end', () => {
              try {
                if (res.statusCode !== 200) throw Error(({400:'Invalid tool arguments',403:'Permission denied',409:'Version or retry conflict'})[res.statusCode] || 'Tool unavailable');
                resolve(JSON.parse(body));
              } catch (err) {reject(err);}
            });
          });
          req.on('error', reject);
          req.end(JSON.stringify(call));
        });
      },
    });
  }
  register('context_whoami','Read the authenticated current user and project.',
    {type:'object', properties:{}, additionalProperties:false},
    {type:'object', properties:Object.fromEntries(['user_id','user_name','project_id','project_name'].map(k=>[k,{type:'string'}])), additionalProperties:false}, false);
  for (const tool of JSON.parse(process.env.OMNI_PROJECT_TOOLS || '[]')) {
    register(tool.name, tool.description, tool.parameters, {type:'object', additionalProperties:true}, true);
  }
};
