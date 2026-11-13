import fs from "node:fs/promises";
import path from "node:path";

// A private publication journal, not another business task queue. Native model
// execution is never replayed here. IDs/content stay immutable across retries.
export function deliveryOutbox(directory, publish) {
 let busy=false;
 const filename = id => path.join(directory, id + ".json");
 async function flush() {
  if(busy)return;
  busy=true;
  try {
   await fs.mkdir(directory,{recursive:true,mode:0o700});
   for(const name of (await fs.readdir(directory)).filter(n=>/^native-delivery-[a-f0-9]{64}\.json$/.test(n))) {
    const file=path.join(directory,name);
    const value=JSON.parse(await fs.readFile(file,"utf8"));
    if(value.payload?.eventId+".json"!==name)throw Error("Invalid native delivery journal identity");
    await publish(value.envelope,value.payload);
    await fs.unlink(file);
   }
  } finally {busy=false}
 }
 return {
  async save(envelope,payload) {
   if(!/^native-delivery-[a-f0-9]{64}$/.test(payload.eventId))throw Error("Invalid native delivery identity");
   await fs.mkdir(directory,{recursive:true,mode:0o700});
   const file=filename(payload.eventId),tmp=file+".tmp";
   await fs.writeFile(tmp,JSON.stringify({envelope,payload}),{mode:0o600});
   await fs.rename(tmp,file);
  },flush
 };
}
