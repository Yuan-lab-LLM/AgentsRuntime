// Test-only host plugin. Never installed into a released Runtime image.
import fs from 'node:fs';
import path from 'node:path';
import { defineTool } from '@deepseek-ai/dsh-tools';
import { createUserMessage } from '@deepseek-ai/dsh-llm';

export default function nativeContract(ctx) {
  ctx.inject(['tools', 'agents'], scoped => {
    scoped.tools.register(defineTool({
      name: 'm0_contract_probe', description: 'Return the native contract fixture sentinel.',
      parameters: { value: { type: 'string', required: true } },
      output: { schema: { type: 'string' }, render: (_args, value) => [{ type: 'text', text: value }] },
      async execute(args, exec) {
        const agent = exec.agent;
        if (!agent || !exec.callId || !agent.session || scoped.agents.get(agent.id) !== agent)
          throw new Error('Missing native execution attribution');
        const methods = Object.fromEntries(['followup', 'cancel', 'whenIdle', 'inject'].map(name => [name, typeof agent[name]]));
        fs.writeFileSync(path.join(process.env.M0_CONTRACT_DIR, 'tool-context.json'), JSON.stringify({
          agent: agent.id, session: agent.session.id, call: exec.callId, methods,
        }));
        exec.deferContext(createUserMessage({
          content: [{ type: 'text', text: 'M0_NONHUMAN_CONTEXT' }],
          source: { kind: 'clawmanager-team', teamId: 'fixture-only' },
        }));
        return 'native-tool-' + args.value;
      },
    }));
  });
}
