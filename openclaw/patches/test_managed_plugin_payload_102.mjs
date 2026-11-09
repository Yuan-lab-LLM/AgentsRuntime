// Image release gate: no network, no model, no instance HOME. Verify required
// dependency closure, and publish deterministic provenance for migration.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import assert from 'node:assert/strict';
const root='/defaults/.openclaw/npm/projects',plugins=[];
const resolvePackage=(from,name)=>{
 for(let dir=from;;dir=path.dirname(dir)){
  const candidate=path.join(dir,'node_modules',name,'package.json');
  if(fs.existsSync(candidate))return fs.realpathSync(candidate);
  if(dir===path.dirname(dir))throw Error('image is missing required dependency '+name+' from '+from);
 }
};
for(const project of fs.readdirSync(root).sort()){
 const base=path.join(root,project);if(!fs.statSync(base).isDirectory())continue;
 const pkg=JSON.parse(fs.readFileSync(base+'/package.json')),lock=JSON.parse(fs.readFileSync(base+'/package-lock.json'));
 assert.equal(Object.keys(pkg.dependencies||{}).length,1);
 const [name,version]=Object.entries(pkg.dependencies)[0];
 assert.equal(lock.packages[''].dependencies[name],version);
 const installed=resolvePackage(base,name),actual=JSON.parse(fs.readFileSync(installed));assert.equal(actual.version,version);
 const seen=new Set(),queue=[installed],proof=[];
 while(queue.length){const file=queue.pop();if(seen.has(file))continue;seen.add(file);const bytes=fs.readFileSync(file),m=JSON.parse(bytes);proof.push(m.name+'@'+m.version+':'+crypto.createHash('sha256').update(bytes).digest('hex'));for(const dep of Object.keys(m.dependencies||{})){if(m.optionalDependencies?.[dep])continue;queue.push(resolvePackage(path.dirname(file),dep))}}
 plugins.push({name,version,project,packages:seen.size,closure_sha256:crypto.createHash('sha256').update(proof.sort().join('\n')).digest('hex')});
}
assert.ok(plugins.length>=3,'missing pinned channel plugins');
if(process.argv.includes('--write-manifest'))fs.writeFileSync('/defaults/.openclaw/managed-plugin-manifest.json',JSON.stringify({version:1,plugins})+'\n');
console.log('PASS managed image plugin closure: '+plugins.map(p=>p.name+'@'+p.version).join(', '));
