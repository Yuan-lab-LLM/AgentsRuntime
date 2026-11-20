import errno
import tempfile
import unittest
from pathlib import Path
from unittest import mock
from test_adapter_contract import adapter

class ReservedTerminalSlotTests(unittest.TestCase):
    def test_oversized_completion_contract_retains_raw_outcome_for_reconciliation(self):
        import asyncio
        import json
        with tempfile.TemporaryDirectory() as root:
            cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:1',team_id='review',member_id='worker',shared_dir=root)
            e={'messageId':'oversize','taskId':'root','assignmentId':'work','revision':1}
            j=adapter.TerminalJournal(cfg);j.admit(e);j.started(e)
            body='x'*(1024*1024+1)
            with self.assertRaisesRegex(ValueError,'1 MiB'):
                asyncio.run(adapter._propose_completion(cfg,e,status='succeeded',summary='Already executed',result_markdown=body,explicit=True))
            row=j.read(e);self.assertEqual(row['state'],'started')
            event=row['publications'][0]['event']
            raw=(Path(root)/event['resultBodyRef'].removeprefix('/team/')).read_text() if event.get('resultBodyRef') else event['resultMarkdown']
            self.assertEqual(json.loads(raw)['resultMarkdown'],body)

    def test_result_uses_preallocated_storage_when_new_allocations_fail(self):
        with tempfile.TemporaryDirectory() as root:
            cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='review',member_id='worker',shared_dir=root)
            e={'messageId':'review','taskId':'root','assignmentId':'work','revision':1}
            journal=adapter.TerminalJournal(cfg);journal.admit(e);journal.started(e)
            event={'event':'completion_proposed','eventId':'outcome','completionId':'complete','attemptId':'stable-attempt','resultMarkdown':'completed external action'}
            original=Path.open
            def no_new_files(file,mode='r',*args,**kwargs):
                if 'terminal-delivery' in str(file) and mode in {'wb','w','a'}:raise OSError(errno.ENOSPC,'No space left on device')
                return original(file,mode,*args,**kwargs)
            with mock.patch.object(Path,'open',no_new_files):journal.stage(e,event)
            after=adapter.TerminalJournal(cfg).read(e)
            self.assertEqual(after['publications'][0]['event'],event)
