import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {pathToFileURL} from 'node:url';
import {stripTurnPatch,applyTurnPatch} from './managed_turn_patch_102.mjs';

const helperName = 'clawmanager-managed-model-reference.mjs';
const artifactName='clawmanager-managed-artifact.json';
const sha256=value=>crypto.createHash('sha256').update(value).digest('hex');
const imports = 'import { prepareManagedRun as cmPrepareManagedRun, isManagedModelError as cmIsManagedModelError, ManagedModelError as CMManagedModelError } from "./'+helperName+'";\n';
const preflight = (signature, variable, key='config') => [signature, signature+'\n\t'+variable+' = await cmPrepareManagedRun('+variable+', "'+key+'");'];
const pickerImports = 'import { filterManagedModelCatalog as cmFilterManagedModelCatalog, isManagedModelError as cmIsManagedModelError } from "./'+helperName+'";\n';
const pickerError = '\t\tif (cmIsManagedModelError(error)) {\n\t\t\trespond(false, void 0, errorShape(ErrorCodes.UNAVAILABLE, error.message));\n\t\t\treturn;\n\t\t}\n';
const pickerSelection = entry => '{ authProfileId: params.authProfileId ?? '+entry+'?.authProfileOverride, modelSelectionLocked: '+entry+'?.modelSelectionLocked }';
export const contracts = [
  {file:'server-shutdown-DqBhRLh6.mjs',hash:'5aad54fa82e96de5db9c60500a245420d3ad3edaaaacabcadae7a5e1edfad01d',
   imports:'import { withManagedNativeShutdown as cmWithManagedNativeShutdown } from "./managed_shutdown_scope.mjs";\n',
   edits:[
    ['\treturn runGatewayShutdownSteps({\n\t\tsteps:', '\treturn cmWithManagedNativeShutdown(() => runGatewayShutdownSteps({\n\t\tsteps:'],
    ['\t\tonError: params.onError\n\t});\n}', '\t\tonError: params.onError\n\t}));\n}'],
   ]},
  {file:'server-close.runtime.js',hash:'c2ec467625f93499cbf758fe6e1d9eb20a768bd43abfe2aedce75ad7c510b895',
   imports:'import { recordManagedNativeCloseResult as cmRecordManagedNativeCloseResult } from "./managed_shutdown_scope.mjs";\n',
   edits:[
    ['\treturn {\n\t\tdurationMs,\n\t\twarnings\n\t};', '\treturn cmRecordManagedNativeCloseResult({\n\t\tdurationMs,\n\t\twarnings\n\t});'],
   ]},
  { file:'models-DIG525mR.mjs', hash:'3905a987dad0f8635260f72970cb5f69293a3cee2138ee87be2dcf34841e95cb', imports:pickerImports, edits:[
    ['\t\tconst projected = scope && params.view !== "provider-config" ? {', '\t\tlet projected = scope && params.view !== "provider-config" ? {'],
    ['\t\tconst policy = prepareOperatorModelPresentation({', '\t\tprojected = await cmFilterManagedModelCatalog(currentConfig, projected, options.signal, '+pickerSelection('scope?.sessionEntry')+');\n\t\tpublicationScope.draftAccountSelection?.assertCurrent();\n\t\tpublicationScope.assertCurrent?.();\n\t\tif (context.getRuntimeConfig() !== currentConfig) throw new PreparedModelRuntimePublicationSupersededError("Model config changed during managed catalog read");\n\t\tconst policy = prepareOperatorModelPresentation({'],
    ['\t\tif (error instanceof UnknownModelCatalogProviderError) {', pickerError+'\t\tif (error instanceof UnknownModelCatalogProviderError) {'],
  ]},
  { file:'chat-metadata-handler-n6SiH0iX.mjs', hash:'1d48cca6aafc926a6b3067755e94b39690119461203b302df4b60f3573d081aa', imports:pickerImports, edits:[
    ['\t\tconst metadata = await context.readChatMetadata(scope);', '\t\tlet metadata = await context.readChatMetadata(scope);'],
    ['\t\tconst policy = prepareOperatorModelPresentation({', '\t\tmetadata = await cmFilterManagedModelCatalog(cfg, metadata, options.signal, '+pickerSelection('scope.sessionEntry')+');\n\t\tscope.draftAccountSelection?.assertCurrent();\n\t\tscope.assertCurrent?.();\n\t\tif (context.getRuntimeConfig() !== cfg) {\n\t\t\trespond(false, void 0, errorShape(ErrorCodes.UNAVAILABLE, "Model config changed; reload the conversation"));\n\t\t\treturn;\n\t\t}\n\t\tconst policy = prepareOperatorModelPresentation({'],
    ['\t} catch (error) {\n\t\tif (error instanceof SessionMutationAuthorizationChangedError)', '\t} catch (error) {\n'+pickerError+'\t\tif (error instanceof SessionMutationAuthorizationChangedError)'],
  ]},
  { file:'chat-history-handler-Bl3TJIG1.mjs', hash:'18999c53d324093198a25a906f95063a69ec87cbaf55b00537ba72dd56f1fe94', imports:pickerImports, edits:[
    ['\t\tconst startupMetadata = method === "chat.startup" ? startupProjection?.metadata : void 0;', '\t\tlet startupMetadata = method === "chat.startup" ? startupProjection?.metadata : void 0;\n\t\tif (startupMetadata) {\n\t\t\ttry {\n\t\t\t\tstartupMetadata = await cmFilterManagedModelCatalog(cfg, startupMetadata, signal, { authProfileId: entry?.authProfileOverride, modelSelectionLocked: entry?.modelSelectionLocked });\n\t\t\t} catch (error) {\n\t\t\t\tif (!cmIsManagedModelError(error)) throw error;\n\t\t\t\trespond(false, void 0, errorShape(ErrorCodes.UNAVAILABLE, error.message));\n\t\t\t\treturn;\n\t\t\t}\n\t\t\tsignal?.throwIfAborted();\n\t\t\tsessionMutationAuthorization?.assertCurrent();\n\t\t\tif (context.getRuntimeConfig() !== cfg) {\n\t\t\t\trespondChatHistoryUnavailable(method, respond, "Model config changed; reload the conversation");\n\t\t\t\treturn;\n\t\t\t}\n\t\t}'],
  ]},
  { file:'embedded-agent-gW15apSG.mjs', hash:'ac6840ab0c45650aa4b60903e860a8d5d705109dd9de574276ca7854fe0755bd', edits:[
    preflight('async function runEmbeddedAgentInternal(paramsInput) {','paramsInput'),
    ['const resolvedParams = {\n\t\t\t\t...params,', 'let resolvedParams = {\n\t\t\t\t...params,'],
    ['\t\t\tif (resolvedParams.trigger !== "manual")', '\t\t\tresolvedParams = await cmPrepareManagedRun(resolvedParams);\n\t\t\tif (resolvedParams.trigger !== "manual")'],
  ]},
  { file:'model-fallback-runner-COVxfi1J.mjs', hash:'5a8fe0206e8b709812f830d9370970791127673817c43c0b4aa11b41739dc712', edits:[
    preflight('async function runWithModelFallback(params) {','params','cfg'),
    ['\t\tconst attemptRun = await runFallbackAttempt({',
     '\t\tconst cmCandidate = await cmPrepareManagedRun({ ...params, provider: candidate.provider, model: candidate.model }, "cfg");\n\t\tif (cmCandidate.provider !== candidate.provider || cmCandidate.model !== candidate.model) throw new CMManagedModelError("模型配置已更新，请重新发送请求；未切换其他模型");\n\t\tconst attemptRun = await runFallbackAttempt({'],
    ['\t\tconst err = attemptRun.error;', '\t\tconst err = attemptRun.error;\n\t\tif (cmIsManagedModelError(err)) throw err;'],
  ]},
  { file:'model-IiOP36F5.mjs', hash:'b8793775f48be480b541f50f5d071a99e2294c531e2dbdd142ff04e0e5794432', edits:[
    ['async function resolveModelAsync(provider, modelId, agentDir, cfg, options) {',
     'async function resolveModelAsync(provider, modelId, agentDir, cfg, options) {\n\tconst cmSelection = await cmPrepareManagedRun({ config: cfg, provider, model: modelId, abortSignal: options?.abortSignal, authProfileId: options?.authProfileId });\n\tprovider = cmSelection.provider;\n\tmodelId = cmSelection.model;'],
  ]},
  { file:'compact-BCDNk0Nn.mjs', hash:'ffa58ac389dbc07a14b330db9ac4548ea5cdf17b1a99b0e90a3ea33679b3ca52', edits:[
    ['\tconst requestedParams = {\n\t\t...paramsBase,', '\tlet requestedParams = {\n\t\t...paramsBase,'],
    ['\tconst requestedAgentIds = resolveSessionAgentIds({', '\trequestedParams = await cmPrepareManagedRun(requestedParams);\n\tconst requestedAgentIds = resolveSessionAgentIds({'],
  ]},
];
function once(source, before, after) {
  if (source.split(before).length !== 2) throw new Error('OpenClaw native model-reference contract changed');
  return source.replace(before,after);
}
export function transform(source, contract) {
  source=stripTurnPatch(source,contract.file);
  const header = contract.imports ?? imports;
  let original=source;
  if(source.startsWith(header)) {
    original=source.slice(header.length);
    for(const [before,after] of [...contract.edits].reverse()) original=once(original,after,before);
  }
  if(crypto.createHash('sha256').update(original).digest('hex')!==contract.hash)
    throw new Error('Unsupported OpenClaw native source: '+contract.file);
  let patched=original;
  for(const [before,after] of contract.edits) patched=once(patched,before,after);
  patched=header+patched;
  if(source.startsWith(header) && source!==patched) throw new Error('Inconsistent model-reference patch');
  return applyTurnPatch(patched,contract.file);
}
export function install(root,verify=false) {
  if(JSON.parse(fs.readFileSync(path.join(root,'package.json'),'utf8')).version!=='2026.10.2')
    throw new Error('OpenClaw model-reference patch requires pinned 2026.10.2');
  const helper=fs.readFileSync(new URL('./managed_model_reference.mjs',import.meta.url),'utf8');
  const taskGuard=fs.readFileSync(new URL('./runtime_task_guard.mjs',import.meta.url),'utf8');
  const turnBridge=fs.readFileSync(new URL('./managed_turn_bridge.mjs',import.meta.url),'utf8');
  const receiptStore=fs.readFileSync(new URL('./task_receipt_store.mjs',import.meta.url),'utf8');
  const renewal=fs.readFileSync(new URL('./runtime_capability_renewal.mjs',import.meta.url),'utf8');
  const writer=fs.readFileSync(new URL('./runtime_writer_guard.mjs',import.meta.url),'utf8');
  const shutdownScope=fs.readFileSync(new URL('./managed_shutdown_scope.mjs',import.meta.url),'utf8');
  const shutdownReceipt=fs.readFileSync(new URL('./shutdown_receipt_store.mjs',import.meta.url),'utf8');
  // Validate every module before writing any file. Unknown native builds fail.
  const planned=contracts.map(contract=>{
    const file=path.join(root,'dist',contract.file);
    const source=fs.readFileSync(file,'utf8');
    const patched=transform(source,contract);
    if(verify && patched!==source) throw new Error('Missing model-reference patch: '+contract.file);
    return {file,patched};
  });
  const helperPath=path.join(root,'dist',helperName);
  const taskGuardPath=path.join(root,'dist','runtime_task_guard.mjs');
  const bridgePath=path.join(root,'dist','managed_turn_bridge.mjs');
  const receiptPath=path.join(root,'dist','task_receipt_store.mjs');
  const renewalPath=path.join(root,'dist','runtime_capability_renewal.mjs');
  const writerPath=path.join(root,'dist','runtime_writer_guard.mjs');
  const shutdownPath=path.join(root,'dist','managed_shutdown_scope.mjs');
  const shutdownReceiptPath=path.join(root,'dist','shutdown_receipt_store.mjs');
  // Generated only after validating pinned native sources. This records
  // integrity, NOT acceptance of the full native execution surface.
  const files=Object.fromEntries([
    ...planned.map(({file,patched})=>[path.basename(file),sha256(patched)]),
    [helperName,sha256(helper)],['runtime_task_guard.mjs',sha256(taskGuard)],
    ['managed_turn_bridge.mjs',sha256(turnBridge)],['task_receipt_store.mjs',sha256(receiptStore)],
    ['runtime_capability_renewal.mjs',sha256(renewal)],
    ['runtime_writer_guard.mjs',sha256(writer)],
    ['managed_shutdown_scope.mjs',sha256(shutdownScope)],
    ['shutdown_receipt_store.mjs',sha256(shutdownReceipt)],
  ].sort(([a],[b])=>a.localeCompare(b,'en')));
  const manifest={schema_version:1,runtime_version:'2026.10.2',adapter_version:'cm-openclaw-native-1',
    release_accepted:false,files};
  const manifestBytes=JSON.stringify(manifest,null,2)+'\n';
  const manifestPath=path.join(root,'dist',artifactName);
  if(verify) {
    if(fs.readFileSync(helperPath,'utf8')!==helper) throw new Error('Model-reference helper differs');
    if(fs.readFileSync(taskGuardPath,'utf8')!==taskGuard) throw new Error('Runtime task guard differs');
    if(fs.readFileSync(bridgePath,'utf8')!==turnBridge) throw new Error('Managed turn bridge differs');
    if(fs.readFileSync(receiptPath,'utf8')!==receiptStore) throw new Error('Receipt store differs');
    if(fs.readFileSync(renewalPath,'utf8')!==renewal) throw new Error('Capability renewal helper differs');
    if(fs.readFileSync(writerPath,'utf8')!==writer) throw new Error('Runtime writer helper differs');
    if(fs.readFileSync(shutdownPath,'utf8')!==shutdownScope) throw new Error('Shutdown scope helper differs');
    if(fs.readFileSync(shutdownReceiptPath,'utf8')!==shutdownReceipt) throw new Error('Shutdown receipt helper differs');
    if(fs.readFileSync(manifestPath,'utf8')!==manifestBytes) throw new Error('Managed artifact manifest differs');
  } else {
    fs.writeFileSync(shutdownReceiptPath,shutdownReceipt);
    fs.writeFileSync(shutdownPath,shutdownScope);
    fs.writeFileSync(writerPath,writer);
    fs.writeFileSync(renewalPath,renewal);
    fs.writeFileSync(receiptPath,receiptStore);
    fs.writeFileSync(bridgePath,turnBridge);
    fs.writeFileSync(taskGuardPath,taskGuard);
    fs.writeFileSync(helperPath,helper);
    for(const {file,patched} of planned) fs.writeFileSync(file,patched);
    // Publish the inventory last; a partial install cannot pass --verify.
    fs.writeFileSync(manifestPath,manifestBytes);
  }
}
export function inspectArtifact(root) {
  install(root,true);
  const bytes=fs.readFileSync(path.join(root,'dist',artifactName));
  return Object.freeze({schema_version:1,runtime_version:'2026.10.2',adapter_version:'cm-openclaw-native-1',
    artifact_sha256:sha256(bytes),integrity_verified:true,node_version:process.version,
    node_version_matches:process.version==='v24.21.0',activation_ready:false,release_accepted:false,
    blockers:['native_runtime_release_acceptance_required',...(process.version==='v24.21.0'?[]:['node_version_mismatch'])]});
}
if(process.argv[1] && import.meta.url===pathToFileURL(path.resolve(process.argv[1])).href) {
  if(process.argv.includes('--require-activation-ready')&&!process.argv.includes('--inspect'))throw new Error('--require-activation-ready requires read-only --inspect');
  if(process.argv.includes('--inspect')&&(process.argv.includes('--patch')||process.argv.includes('--allow-legacy')))throw new Error('--inspect cannot patch or bypass pinned native validation');
  const root=process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
  const version=JSON.parse(fs.readFileSync(path.join(root,'package.json'),'utf8')).version;
  const [year,month,day]=String(version).split('-')[0].split('.').map(Number);
  const legacy=Number.isInteger(year)&&Number.isInteger(month)&&Number.isInteger(day)&&
    (year<2026 || year===2026&&(month<10 || month===10&&day<2));
  if(process.argv.includes('--allow-legacy')&&legacy) {
    console.log('Legacy OpenClaw retained unchanged; native model-reference restoration unavailable on '+version);
  } else {
    if(process.argv.includes('--inspect')) {
      // Read only: missing/drifted files are errors, never repaired here.
      const report=inspectArtifact(root);
      console.log(JSON.stringify(report,null,2));
      if(process.argv.includes('--require-activation-ready')&&!report.activation_ready)process.exitCode=2;
    } else {
      install(root,process.argv.includes('--verify'));
      console.log('Verified OpenClaw 2026.10.2 managed model-reference native entrypoints');
    }
  }
}
