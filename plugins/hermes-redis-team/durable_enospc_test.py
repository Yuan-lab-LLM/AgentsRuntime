import asyncio
import errno
import json
import os
import subprocess
import sys
from pathlib import Path
from test_adapter_contract import adapter

root=Path(os.environ['RUNTIME_ENOSPC_ROOT'])
assert 'cm-runtime-enospc' in str(root)
cfg=adapter.RedisTeamSettings(enabled=True,redis_url=os.environ['HERMES_REDIS_TEST_URL'],team_id='enospc-hermes',member_id='worker',role='worker',shared_dir=str(root/'hermes'))
e={'messageId':'accepted-native-work','taskId':'root-enospc','rootTaskId':'root-enospc','assignmentId':'work','revision':1,'text':'x'*64000}
journal=adapter.TerminalJournal(cfg)
if len(sys.argv)>1:
    original_write,original_sync=os.pwrite,os.fsync
    commits=0
    def write(fd,data,offset):
        if sys.argv[1]=='crash-before' and data.startswith(b'CMJ3 '):os._exit(88)
        return original_write(fd,data,offset)
    def sync(fd):
        global commits
        original_sync(fd);commits+=1
        if commits==2 and sys.argv[1]=='crash-after':os._exit(89)
    os.pwrite,os.fsync=write,sync
    journal.stage(e,{'event':'reply','eventId':sys.argv[1],'text':'post-commit retained outcome'})
    raise AssertionError('crash hook did not execute')
async def main():
    adapter.ensure_team_dirs(cfg);adapter._persist_active_envelope(cfg,e)
    journal.admit(e);journal.started(e)
    filler=root/'hermes-fill'
    try:
        with filler.open('wb',buffering=0) as handle:
            try:
                while True:handle.write(b'x'*(1024*1024))
            except OSError as error:assert error.errno==errno.ENOSPC
            handle.flush();os.fsync(handle.fileno())
        assert os.statvfs(root).f_bavail==0
        body='C'*(1024*1024-16384)
        outcome=await adapter._propose_completion(cfg,e,status='succeeded',summary='Completed external action',result_markdown=body,explicit=True)
        assert outcome['durable'] and not outcome['published']
        assert adapter.TerminalJournal(cfg).read(e)['publications'][0]['event']['resultMarkdown']==body
        for mode,status,count in [('crash-before',88,1),('crash-after',89,2)]:
            result=subprocess.run([sys.executable,__file__,mode],capture_output=True,text=True)
            assert result.returncode==status,result.stderr
            assert len(adapter.TerminalJournal(cfg).read(e)['publications'])==count
        filler.unlink()
        class Redis:
            async def command(self,*args):return [1,'1-0']
        await adapter.TerminalJournal(cfg).flush(Redis(),e)
        row=adapter.TerminalJournal(cfg).read(e)
        artifact=cfg.shared_path/row['publications'][0]['event']['resultBodyRef'].removeprefix('/team/')
        assert artifact.read_text()==body
        print(json.dumps({'runtime':'hermes','realENOSPC':True,'actualCompletionTool':True,'resultBytes':len(body),'crashBeforeCommit':'previous outcome retained','crashAfterCommit':'new outcome retained','materializedAfterRecovery':True,'businessCalls':1}))
    finally:
        filler.unlink(missing_ok=True)
asyncio.run(main())
