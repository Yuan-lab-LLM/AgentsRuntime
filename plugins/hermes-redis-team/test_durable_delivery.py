import asyncio
import tempfile
import unittest
from unittest import mock
from test_adapter_contract import adapter

class DurableDeliveryTests(unittest.TestCase):
    def test_execution_claim_is_atomic_and_context_handoff_never_reopens_serial_admission(self):
        from concurrent.futures import ThreadPoolExecutor
        import threading
        with tempfile.TemporaryDirectory() as root:
            cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='claims',member_id='worker',shared_dir=root)
            e={'messageId':'same','taskId':'root'};j=adapter.TerminalJournal(cfg);j.admit(e)
            gate=threading.Barrier(2)
            def claim():
                gate.wait()
                return adapter.TerminalJournal(cfg).started(e)
            with ThreadPoolExecutor(max_workers=2) as pool:
                first=pool.submit(claim);second=pool.submit(claim)
                self.assertEqual(sorted([first.result(),second.result()]),[False,True])
            j.context(e,'queued',owned=True)
            self.assertFalse(j.started(e))
            self.assertTrue(j.context(e,'submitting'))
            self.assertFalse(j.started(e))
            j.context(e,'queued')  # Native explicitly declined; context retains handoff.
            self.assertFalse(j.started(e))
            self.assertTrue(j.context(e,'submitting'))
            j.context(e,'submitted')
            self.assertFalse(adapter.TerminalJournal(cfg).started(e))

    def test_stale_flusher_does_not_restore_compacted_or_reclaimed_record(self):
        async def run(closed):
            import json
            with tempfile.TemporaryDirectory() as root:
                cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='stale',member_id='worker',shared_dir=root)
                j=adapter.TerminalJournal(cfg)
                e={'messageId':'original','taskId':'root','redisId':'1-0'}
                j.stage(e,{'event':'completion_proposed','eventId':'evt','completionId':'c','attemptId':'a','resultMarkdown':'full original outcome'})
                async def concurrent(*args):
                    if args[0]=='EVAL':return [1,'2-0']
                    if str(args[1]).endswith(':replay-source-closed'):
                        return json.dumps({'schema':1,'closed_before_ms':9,'committed_cursor':'9-0','proof_sha256':'a'*64,'verified_at':'2026-11-18T00:00:00Z'}) if closed else None
                    if ':completion-ack:' in str(args[1]):return json.dumps({'decision':'accepted'})
                    return None
                async def publish(*args):
                    self.assertEqual(args[0],'EVAL')
                    other=adapter.TerminalJournal(cfg)
                    other.accepted(e)
                    await other.flush(mock.Mock(command=concurrent),e,True)
                    return [0,'2-0']
                await j.flush(mock.Mock(command=publish),e)
                record=j.read(e)
                if closed:self.assertIsNone(record)
                else:
                    self.assertTrue(record['compact'])
                    self.assertEqual(record['publications'][0]['decision'],'accepted')
                    self.assertEqual(record['publications'][0]['event']['resultBodyRef'],record['resultRef'])
        asyncio.run(run(False));asyncio.run(run(True))

    def test_admission_result_and_replay_survive_restart(self):
        with tempfile.TemporaryDirectory() as root:
            cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='durable',member_id='worker',shared_dir=root)
            envelope={'messageId':'m1','idempotencyKey':'m1','taskId':'t1','assignmentId':'a1'}
            self.assertTrue(hasattr(adapter,"TerminalJournal"),"durable terminal journal must exist before dispatch")
            j=adapter.TerminalJournal(cfg,global_limit=2,team_limit=2,member_limit=1)
            self.assertEqual(j.admit(envelope)['state'],'admitted')
            j.started(envelope)
            self.assertEqual(adapter.TerminalJournal(cfg).read(envelope)['state'],'started')
            with self.assertRaisesRegex(RuntimeError,'capacity'):
                j.admit({**envelope,'messageId':'m2','idempotencyKey':'m2'})
            event={'event':'completion_proposed','eventId':'evt1','completionId':'c1','attemptId':'a1','resultMarkdown':'actual completed work'}
            j.stage(envelope,event)
            redis=mock.Mock(command=mock.AsyncMock(side_effect=RuntimeError('OOM command not allowed')))
            with self.assertRaisesRegex(RuntimeError,'OOM'):
                asyncio.run(j.flush(redis))
            self.assertEqual(j.read(envelope)['publications'][0]['event'],event)
            redis.command=mock.AsyncMock(side_effect=lambda *args: None if args[0]=='GET' else [1,'1-0'])
            asyncio.run(adapter.TerminalJournal(cfg).flush(redis))
            asyncio.run(adapter.TerminalJournal(cfg).flush(redis))
            self.assertEqual(sum(call.args[0]=='EVAL' for call in redis.command.call_args_list),1)
            self.assertTrue(any(call.args[0]=='EVAL' for call in redis.command.call_args_list))
            j.accepted(envelope)
            self.assertEqual(adapter.TerminalJournal(cfg).admit(envelope)['state'],'accepted')

    def test_completed_receipts_compact_and_only_verified_closed_sources_reclaim(self):
        async def run():
            import json
            from pathlib import Path
            with tempfile.TemporaryDirectory() as root:
                cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='capacity',member_id='worker',shared_dir=root)
                j=adapter.TerminalJournal(cfg)
                closure=None
                async def command(*args):
                    if args[0]=='EVAL': return [1,'900-0']
                    if str(args[1]).endswith(':replay-source-closed'): return json.dumps(closure) if closure else None
                    if ':completion-ack:' in str(args[1]): return json.dumps({'decision':'accepted'})
                    return None
                redis=mock.Mock(command=command)
                for index in range(501):
                    e={'messageId':f'm{index}','taskId':f't{index}','redisId':f'{index+1}-0'}
                    j.admit(e);j.started(e)
                    j.stage(e,{'event':'completion_proposed','eventId':f'evt{index}','completionId':f'c{index}','attemptId':f'a{index}','status':'succeeded','resultMarkdown':f'Actual outcome {index}'})
                    j.accepted(e);await j.flush(redis,e,True)
                    self.assertTrue(j.read(e)['compact'])
                files=list(j.directory.glob('*.json'))
                self.assertEqual(len(files),501)
                self.assertTrue(all(file.stat().st_size==4096 for file in files))
                self.assertEqual(j.admit({'messageId':'m0'})['state'],'accepted')
                for index in range(256):j.admit({'messageId':f'pending{index}','taskId':f'pending{index}','redisId':f'{index+1}-0'})
                with self.assertRaisesRegex(RuntimeError,'capacity'):j.admit({'messageId':'overflow'})
                closure={'schema':1,'closed_before_ms':99999,'committed_cursor':'99999-0','proof_sha256':'a'*64,'verified_at':'2026-11-18T00:00:00Z'}
                for _ in range(6):await j.flush(redis)
                self.assertIsNone(j.read({'messageId':'m0'}))
                self.assertEqual(j.read({'messageId':'pending0'})['state'],'admitted')
                other=adapter.TerminalJournal(adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='capacity',member_id='other',shared_dir=root))
                body='完整结果'*30000;e={'messageId':'large','taskId':'large'}
                pub=other.stage(e,{'event':'completion_proposed','eventId':'large','completionId':'large','attemptId':'large','resultMarkdown':body,'result':body})
                self.assertEqual(pub['event']['resultBodyBytes'],len(body.encode()))
                self.assertEqual((Path(root)/pub['event']['resultBodyRef'].removeprefix('/team/')).read_text(),body)
                self.assertLess(len(json.dumps(pub['event'],ensure_ascii=False).encode()),65536)
        asyncio.run(run())

    def test_later_completion_closes_300_prior_turns_only_in_exact_scope(self):
        async def run():
            import json
            with tempfile.TemporaryDirectory() as root:
                cfg=adapter.RedisTeamSettings(enabled=True,redis_url='redis://localhost:6379',team_id='multi',member_id='worker',shared_dir=root)
                j=adapter.TerminalJournal(cfg);ack={}
                async def command(*args):
                    if args[0]=='EVAL':return [1,'900-0']
                    if ':completion-ack:' in str(args[1]):return json.dumps(ack)
                    return None
                redis=mock.Mock(command=command)
                for index in range(300):
                    earlier={'messageId':f'earlier{index}','taskId':f'scope{index}','rootTaskId':f'scope{index}','assignmentId':f'assignment{index}','revision':1}
                    later={**earlier,'messageId':f'later{index}'}
                    j.admit(earlier);j.started(earlier);j.admit(later);j.started(later)
                    ack={'teamId':'multi','memberId':'worker','rootTaskId':later['rootTaskId'],'completionId':f'closed{index}','attemptId':f'attempt{index}','decision':'accepted'}
                    j.stage(later,{'event':'completion_proposed','eventId':f'later{index}','completionId':ack['completionId'],'attemptId':ack['attemptId'],'assignmentId':later['assignmentId'],'revision':1,'status':'succeeded'})
                    j.accepted(later);await j.flush(redis,later,True);await j.flush(redis,earlier,True)
                    self.assertTrue(j.read(earlier)['compact']);self.assertTrue(j.read(earlier)['executionMayHaveOccurred'])
                for change in ({'rootTaskId':'wrong-root'},{'assignmentId':'wrong-assignment'},{'revision':2}):
                    wrong={'messageId':'mismatch'+json.dumps(change),'taskId':'scope299','rootTaskId':'scope299','assignmentId':'assignment299','revision':1,**change}
                    j.admit(wrong);j.started(wrong);await j.flush(redis,wrong,True)
                    self.assertEqual(j.read(wrong)['state'],'started')
        asyncio.run(run())
