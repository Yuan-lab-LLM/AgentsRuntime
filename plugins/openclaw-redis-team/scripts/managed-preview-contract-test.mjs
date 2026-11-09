import assert from "node:assert/strict";
import fs from "node:fs/promises";
import http from "node:http";
import path from "node:path";
const file = path.resolve(import.meta.dirname, "../dist/index.js");
const source = (await fs.readFile(file,"utf8")).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";', 'const definePluginEntry = (x) => x;');
const plugin = await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport { previewUrlForTeamArtifact, optionalPreviewFields, isManagedInteractivePreviewUrl };').toString('base64')}`).catch(error => { console.error(error.name, error.message); process.exit(1); });
process.on("uncaughtException", error => { console.error(error.name, error.message); process.exit(1); });
process.env.CLAWMANAGER_NETWORK_ISOLATED="1";
process.env.CLAWMANAGER_MANAGED_SERVICES_URL="http://attacker.invalid:9999";
process.env.HTTP_PROXY="http://attacker.invalid:8888";
process.env.CLAWMANAGER_TEAM_TOKEN="legacy-key-must-not-sign";
let status=200;
let response={url:"http://clawmanager-egress-proxy.system.svc.cluster.local:3128/v3/interactive/42/_/opaque/index.html",mode:"interactive",expiresAt:new Date(Date.now()+60000).toISOString()};
const requests=[];
const server=http.createServer(async(req,res)=>{
 let body="";for await(const chunk of req)body+=chunk;
 requests.push({path:req.url,headers:req.headers,body:JSON.parse(body)});
 res.writeHead(status,{"Content-Type":"application/json"});res.end(JSON.stringify(response));
});
await new Promise(resolve=>server.listen(0,"127.0.0.1",resolve));
const original=http.request;
http.request=function(url,options,callback){
 assert.equal(String(url),"http://clawmanager-local-services.invalid:31282/team/preview-sign");
 return original.call(this,new URL(`http://127.0.0.1:${server.address().port}/team/preview-sign`),options,callback);
};
try{
 const result=await plugin.previewUrlForTeamArtifact({sharedDir:"/team",teamId:"untrusted"},"/team/site/index.html");
 assert.equal(result.url,response.url);
 assert.deepEqual(requests[0].body,{path:"site/index.html",mode:"interactive"});
 assert.equal(requests[0].headers.authorization,undefined);
 assert.equal(requests[0].headers["proxy-authorization"],undefined);
 assert.equal(plugin.isManagedInteractivePreviewUrl(result.url),true);
 delete process.env.CLAWMANAGER_TEAM_TOKEN;
 delete process.env.CLAWMANAGER_NETWORK_ISOLATED;
 process.env.CLAWMANAGER_MANAGED_SERVICES_URL="http://clawmanager-local-services.invalid:31282";
 assert.equal((await plugin.previewUrlForTeamArtifact({sharedDir:"/team"},"/team/site/index.html")).url,response.url);
 status=302;
 await assert.rejects(()=>plugin.previewUrlForTeamArtifact({sharedDir:"/team"},"/team/site/index.html"));
 status=200;
 assert.deepEqual(await plugin.optionalPreviewFields({sharedDir:"/team"},"/team/site/index.html"),{previewUrl:response.url});
 status=503;
 await assert.rejects(()=>plugin.previewUrlForTeamArtifact({sharedDir:"/team"},"/team/site/index.html"));
 status=200;response={...response,url:"http://attacker.invalid/v3/interactive/42/x"};
 await assert.rejects(()=>plugin.previewUrlForTeamArtifact({sharedDir:"/team"},"/team/site/index.html"));
 response={...response,url:"x".repeat(9000)};
 await assert.rejects(()=>plugin.previewUrlForTeamArtifact({sharedDir:"/team"},"/team/site/index.html"));
 console.log("managed preview protected contract PASS");
}finally{http.request=original;await new Promise(resolve=>server.close(resolve));}
