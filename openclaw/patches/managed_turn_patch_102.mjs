// Applied after the reference patch, reversible before its pristine SHA check.
const header='import { withManagedTurn as cmWithManagedTurn, attachManagedTaskHeaders as cmAttachManagedTaskHeaders, joinManagedCleanup as cmJoinManagedCleanup, hasManagedTurn as cmHasManagedTurn } from "./managed_turn_bridge.mjs";\n';
const edits={
 'model-fallback-runner-COVxfi1J.mjs':[
  ['\tconst deferredSuspension = {};','\treturn cmWithManagedTurn(params, async (params) => {\n\tconst deferredSuspension = {};'],
  ['\t\tthrow err;\n\t}\n}\nasync function runWithModelFallbackInternal','\t\tthrow err;\n\t}\n\t}, "cfg");\n}\nasync function runWithModelFallbackInternal'],
 ],
 'embedded-agent-gW15apSG.mjs':[
  ['\tparamsInput = await cmPrepareManagedRun(paramsInput, "config");','\tparamsInput = await cmPrepareManagedRun(paramsInput, "config");\n\treturn cmWithManagedTurn(paramsInput, async (paramsInput) => {'],
  ['\t\trevokeMessageActionTurnCapability(recoveryMessageActionTurnCapability);\n\t});\n}', '\t\trevokeMessageActionTurnCapability(recoveryMessageActionTurnCapability);\n\t});\n\t});\n}'],
 ],
 'model-IiOP36F5.mjs':[
  ['\treturn preparedModelRuntime ? await withPluginRuntimeGenerationScope(preparedModelRuntime, resolve) : await resolve();', '\tconst cmResolved = preparedModelRuntime ? await withPluginRuntimeGenerationScope(preparedModelRuntime, resolve) : await resolve();\n\treturn cmAttachManagedTaskHeaders(cmResolved, cfg, options);'],
 ],
 'compact-BCDNk0Nn.mjs':[
  ['\t\t\tif (hasModelFallbackStop(err)) throw err;', '\t\t\tif (cmIsManagedModelError(err) || hasModelFallbackStop(err)) throw err;'],
  ['\t\t} catch (err) {\n\t\t\treturn {\n\t\t\t\tok: false,\n\t\t\t\tcompacted: false,\n\t\t\t\treason: isFallbackSummaryError(err)', '\t\t} catch (err) {\n\t\t\tif (cmIsManagedModelError(err)) throw err;\n\t\t\treturn {\n\t\t\t\tok: false,\n\t\t\t\tcompacted: false,\n\t\t\t\treason: isFallbackSummaryError(err)'],
  ['\trequestedParams = await cmPrepareManagedRun(requestedParams);', '\trequestedParams = await cmPrepareManagedRun(requestedParams);\n\treturn cmWithManagedTurn(requestedParams, async (requestedParams) => {'],
  ['\ttrackOwner(async () => {\n\t\tconst closeWork = () => context', '\tconst cmOwner = trackOwner(async () => {\n\t\tconst closeWork = () => context'],
  ['\ttrackOwner(async () => {\n\t\tconst closeWork = () => {', '\tconst cmOwner = trackOwner(async () => {\n\t\tconst closeWork = () => {'],
  ['\t}).catch(callerResult.reject);\n\treturn await callerResult.promise;', '\t}).catch(error => { callerResult.reject(error); if (cmHasManagedTurn()) throw error; });\n\treturn await callerResult.promise;', 2],
  ['\treturn await callerResult.promise;\n}\n//#endregion\n//#region src/agents/embedded-agent-runner/run/prepared-runtime-context.ts', '\treturn await cmJoinManagedCleanup(callerResult.promise, cmOwner);\n}\n//#endregion\n//#region src/agents/embedded-agent-runner/run/prepared-runtime-context.ts'],
  ['\treturn await callerResult.promise;\n}\n//#endregion\nexport { resolveTieredModel', '\treturn await cmJoinManagedCleanup(callerResult.promise, cmOwner);\n\t}, "config", "compaction");\n}\n//#endregion\nexport { resolveTieredModel'],
 ],
};
function once(source,before,after,count=1) {
 if(source.split(before).length!==count+1)throw new Error('OpenClaw native task contract changed');
 return source.split(before).join(after);
}
export function stripTurnPatch(source,file) {
 if(!source.startsWith(header))return source;
 if(!edits[file])throw new Error('Unexpected native task patch');
 let result=source.slice(header.length);
 for(const [before,after,count] of [...edits[file]].reverse())result=once(result,after,before,count);
 return result;
}
export function applyTurnPatch(source,file) {
 if(!edits[file])return source;
 let result=source;
 for(const [before,after,count] of edits[file])result=once(result,before,after,count);
 return header+result;
}
