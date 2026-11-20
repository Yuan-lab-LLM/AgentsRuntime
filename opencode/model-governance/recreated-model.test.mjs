import test from 'node:test';
import assert from 'node:assert/strict';
import { createGovernance, ModelUnavailable } from './governance.mjs';

test('recreated display name is visible but old session name cannot bind to its new deployment', async () => {
  const entry = {id:'same name',stable_id:'cm-deploy-v1-new',legacy_name_unavailable:true,context_window:256000,tools:true};
  const g = createGovernance({env:{CLAWMANAGER_LLM_BASE_URL:'https://gateway.test/llm',CLAWMANAGER_LLM_API_KEY:'test',CLAWMANAGER_GATEWAY_GENERATION:'1'},fetcher:async()=>Response.json({models:[entry]})});
  const template = {id:'auto',providerID:'clawmanager',api:{id:'auto'},capabilities:{},limit:{context:8192}};
  const list = await g.projectCatalog([template]);
  assert.equal(list[0].id,'cm-deploy-v1-new');
  assert.equal(list[0].name,'same name');
  await assert.rejects(g.resolveCatalogModel([template],'clawmanager','same name'),ModelUnavailable);
  const current = await g.resolveCatalogModel([template],'clawmanager','cm-deploy-v1-new');
  assert.equal(current.api.id,'cm-deploy-v1-new');
});
