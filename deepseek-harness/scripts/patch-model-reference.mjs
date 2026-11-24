import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { readFileSync, writeFileSync, existsSync, realpathSync } from 'node:fs';
import { resolve, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const helperName = 'clawmanager-model-reference.mjs';
const helperImport = `import { resolveManagedSelection, filterManagedModels } from './${helperName}';\n`;
const taskImports = {
  'dsh-agent-loop': "import { withManagedTurn } from '../../dsh-llm/lib/clawmanager-model-reference.mjs';\n",
  'dsh-llm-pi-ai': "import { managedRequestHeaders } from '../../dsh-llm/lib/clawmanager-model-reference.mjs';\n",
};
export const contracts = {
  'dsh-agent-loop': { hash: 'cf57e3ecd487ac5c2553455aaa5cef2c6fb41e6bd61be5aa455675d1d7193033', replacements: [
    ['\tasync turn() {', '\tasync turn() {\n\t\treturn withManagedTurn(this, () => this.clawmanagerTurn());\n\t}\n\tasync clawmanagerTurn() {'],
    ['\t\tconst prepared = await ctx.tools[TOOL_RUNTIME_SCHEDULER].prepare(call.exec);\n\t\tthrowSchedulerFailure();',
     '\t\tconst prepared = await ctx.tools[TOOL_RUNTIME_SCHEDULER].prepare(call.exec);\n\t\tthrowSchedulerFailure();\n\t\t// Managed instances: cancellation may arrive while tool prepare awaits.\n\t\tif (process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL === "1" && !/^(1|true|yes|on|enabled)$/i.test((process.env.CLAWMANAGER_TEAM_ENABLED ?? "").trim())) signal.throwIfAborted();'],
  ] },
  'dsh-llm': { hash: 'd18d8ae4c8d961e628708c1e4698e09716a0ebb80e55a672e0bde1029bec1155', replacements: [
    ['\t\tasync resolveModelInfo(provider, model, signal) {\n\t\t\treturn this.resolveModelInfoFor(this.registration(provider), model, signal);',
     '\t\tasync resolveManagedModelReference(selection, signal) {\n\t\t\treturn resolveManagedSelection(this, selection, signal, LlmError);\n\t\t}\n\t\tasync resolveModelInfo(provider, model, signal) {\n\t\t\tconst selected = await this.resolveManagedModelReference({ provider, model }, signal);\n\t\t\treturn this.resolveModelInfoFor(this.registration(selected.provider), selected.model, signal);'],
    ['\t\tasync resolveCallConfig(config, signal) {', '\t\tasync resolveCallConfig(config, signal) {\n\t\t\tconfig = await this.resolveManagedModelReference(config, signal);'],
    ['\t\tasync prepareCall(config, signal, configure) {', '\t\tasync prepareCall(config, signal, configure) {\n\t\t\tconfig = await this.resolveManagedModelReference(config, signal);'],
    ['\t\t\t\tconst registration = prepared?.registration ?? this.registration(options.provider);',
     '\t\t\t\tconst current = await this.resolveManagedModelReference(options, options.signal);\n\t\t\t\toptions.signal?.throwIfAborted();\n\t\t\t\tif (prepared && (current.provider !== options.provider || current.model !== options.model || this.adapters.get(options.provider) !== prepared.registration)) throw new LlmError("prepared model route is no longer available", "INVALID_PREPARED_CALL");\n\t\t\t\toptions = current;\n\t\t\t\tconst registration = prepared?.registration ?? this.registration(options.provider);'],
    ['\t\t\tconst models = await this.registration(provider).adapter.listModels(provider);',
     '\t\t\tconst models = await filterManagedModels(this, provider, await this.registration(provider).adapter.listModels(provider), LlmError);'],
  ] },
  'dsh-llm-pi-ai': { hash: '99b5c20830a52f69641df8a18a39206079b0c725201958cce2c4a934f1d4f7cd', replacements: [
    ['headers: requestHeaders(profile.headers)', 'headers: managedRequestHeaders(profile.baseURL, apiKey, requestHeaders(profile.headers))'],
    ['\timageRequestTarget(provider, model, image) {', `\tasync clawmanagerModelProfile(provider) {
\t\tconst snapshot = this.current();
\t\tconst profile = snapshot.profiles.get(provider);
\t\tif (!profile || !["CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY"].includes(profile.apiKeyEnv) ||
\t\t\tString(profile.baseURL ?? "").replace(/\\/+$/, "") !== String(process.env.CLAWMANAGER_LLM_BASE_URL ?? "").replace(/\\/+$/, "")) return;
\t\treturn { baseURL: profile.baseURL, apiKey: await this.config.resolveApiKey(provider, profile), models: snapshot.models.getModels(provider) };
\t}
\timageRequestTarget(provider, model, image) {`],
  ] },
  'dsh-api-session-controller': { hash: 'a9801acb577b03053c5d5a3191ff555c5f1d791d0acddec113bea4511e33b386', replacements: [
    ['async function modelAvailable(ctx, selection) {', `async function modelAvailable(ctx, selection) {
\ttry {
\t\tselection = await ctx.llm.resolveManagedModelReference(selection);
\t} catch (error) {
\t\tthrow new RemoteError("session/model-unavailable", error instanceof Error ? error.message : "当前模型不可用", { provider: selection.provider, model: selection.model });
\t}`],
  ] },
};
const digest = source => createHash('sha256').update(source).digest('hex');
export function transform(name, source) {
  const contract = contracts[name];
  if (!contract) throw new Error('Unknown native package');
  let original = source;
  if (name === 'dsh-llm' && original.startsWith(helperImport)) original = original.slice(helperImport.length);
  if (taskImports[name] && original.startsWith(taskImports[name])) original = original.slice(taskImports[name].length);
  for (const [before, after] of [...contract.replacements].reverse()) original = original.replace(after, before);
  if (digest(original) !== contract.hash) throw new Error(`Unreviewed Harness native source: ${name}`);
  let result = original;
  for (const [before, after] of contract.replacements) {
    if (result.split(before).length !== 2) throw new Error(`Native model contract changed: ${name}`);
    result = result.replace(before, after);
  }
  return (name === 'dsh-llm' ? helperImport : taskImports[name] ?? '') + result;
}

export function install(root, verify = false) {
  const packagePath = join(realpathSync(root), 'package.json');
  const pkg = JSON.parse(readFileSync(packagePath, 'utf8'));
  if (pkg.name !== '@deepseek-ai/dsh' || pkg.version !== '0.2.1-rc.3') throw new Error('Requires Harness 0.2.1-rc.3');
  const requireFromDSH = createRequire(packagePath);
  const files = Object.keys(contracts).map(name => {
    const target = realpathSync(requireFromDSH.resolve('@deepseek-ai/' + name));
    const source = readFileSync(target, 'utf8');
    return { name, target, source, result: transform(name, source) };
  });
  const targetHelper = join(dirname(files.find(file => file.name === 'dsh-llm').target), helperName);
  for (const name of Object.keys(taskImports)) {
    if (resolve(dirname(files.find(file => file.name === name).target), '../../dsh-llm/lib', helperName) !== targetHelper)
      throw new Error('Harness task bridge requires reviewed sibling package layout');
  }
  const helper = readFileSync(new URL('./managed-model-reference.mjs', import.meta.url), 'utf8');
  // Validate all modules before changing any build artifact. Never modify home
  // directories, session events, profile files or an unrecognized version.
  if (verify) {
    if (files.some(file => file.source !== file.result) || !existsSync(targetHelper) || readFileSync(targetHelper, 'utf8') !== helper) throw new Error('Model reference patch is absent or differs');
    return;
  }
  writeFileSync(targetHelper, helper);
  for (const file of files) writeFileSync(file.target, file.result);
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  install(resolve(process.argv[2] ?? '/usr/local/lib/node_modules/@deepseek-ai/dsh'), process.argv.includes('--verify'));
  console.log('Harness model reference contracts verified');
}
