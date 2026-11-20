"""Actual Hermes inbound/completion handlers, isolated noeviction Redis/AOF only."""
import asyncio
import json
import os
import sys
import types
from pathlib import Path
from unittest import mock
from test_adapter_contract import adapter

async def main():
    root=Path(os.environ['RUNTIME_DURABLE_TEST_STATE']);root.mkdir(parents=True,exist_ok=True)
    settings=adapter.RedisTeamSettings(enabled=True,auto_run=True,redis_url=os.environ['HERMES_REDIS_TEST_URL'],team_id='runtime-durable-hermes'+os.getenv('RUNTIME_DURABLE_TEST_SUFFIX',''),member_id='worker',role='worker',shared_dir=str(root))
    redis=adapter.AsyncRedisClient(settings.redis_url);await redis.connect()
    assert await redis.command('GET','cm:test:runtime-durable:isolated')=='runtime-durable-20261118'
    with mock.patch.object(adapter,'load_settings',return_value=settings):
        instance=adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
    instance._redis=redis
    calls_file=root/'business-calls';envelope_file=root/'test-envelope.json'
    try:
        if sys.argv[1]=='prepare':
            calls_file.write_text('0')
            envelope={'messageId':'hermes-durable-inbound','idempotencyKey':'hermes-durable-inbound','teamId':settings.team_id,'from':'leader','to':'worker','taskId':'root-hermes-1','rootTaskId':'root-hermes-1','assignmentId':'work-1','revision':1,'text':'Execute exactly once'}
            await redis.command('XGROUP','CREATE',adapter.inbox_key(settings),settings.consumer_group,'0','MKSTREAM')
            await redis.command('XADD',adapter.inbox_key(settings),'*','payload',json.dumps(envelope))
            raw=await redis.command('XREADGROUP','GROUP',settings.consumer_group,settings.member_id,'STREAMS',adapter.inbox_key(settings),'>')
            envelope=adapter._parse_stream_response(raw)[0];envelope_file.write_text(json.dumps(envelope))
            async def dispatch(received,**kwargs):
                calls_file.write_text(str(int(calls_file.read_text())+1))
                await redis.command('CONFIG','SET','maxmemory','1')
                outcome=await adapter._propose_completion(settings,received,status='succeeded',summary='Actual completed work',result_markdown='full Hermes result survives OOM',explicit=True)
                assert outcome['durable'] is True and outcome['published'] is False
            instance._dispatch_envelope=dispatch
            try:
                await instance._handle_redis_message(envelope)
                raise AssertionError('processed SET must fail during OOM')
            except adapter.RespError as error:
                assert 'OOM' in str(error)
            row=adapter.TerminalJournal(settings).read(envelope)
            assert row['publications'][0]['event']['resultMarkdown']=='full Hermes result survives OOM'
            assert row['publications'][0]['streamId']==''
            assert int(calls_file.read_text())==1
            assert (await redis.command('XPENDING',adapter.inbox_key(settings),settings.consumer_group))[0]==1
            await redis.command('CONFIG','SET','maxmemory','16777216')
            print(json.dumps({'phase':'prepare','runtime':'hermes','actualCompletionTool':True,'realNoevictionOOM':True,'pending':1,'businessCalls':1}))
        else:
            envelope=json.loads(envelope_file.read_text())
            before=adapter.TerminalJournal(settings).read(envelope);attempt=before['publications'][0]['event']['attemptId']
            async def dispatch(*args,**kwargs):
                raise AssertionError('business must not rerun after process restart')
            instance._dispatch_envelope=dispatch
            await instance._handle_redis_message(envelope)
            await instance._handle_redis_message(envelope)
            row=adapter.TerminalJournal(settings).read(envelope)
            assert row['publications'][0]['event']['attemptId']==attempt
            assert int(calls_file.read_text())==1
            assert (await redis.command('XPENDING',adapter.inbox_key(settings),settings.consumer_group))[0]==0
            raw=await redis.command('XRANGE',adapter.events_key(settings),'-','+')
            events=[json.loads(item[1][item[1].index('payload')+1]) for item in raw]
            terminals=[event for event in events if event['event']=='completion_proposed']
            assert len(terminals)==1 and terminals[0]['resultMarkdown']=='full Hermes result survives OOM'
            lost={'messageId':'lost-reply','taskId':'lost-reply'}
            journal=adapter.TerminalJournal(settings);journal.admit(lost);journal.started(lost)
            journal.stage(lost,{'event':'completion_proposed','eventId':'lost-reply','completionId':'lost-reply','attemptId':'lost-reply','resultMarkdown':'completed before lost reply'})
            async def lost_command(*args):
                result=await redis.command(*args)
                if args[0]=='EVAL': raise ConnectionError('lost response after Redis committed')
                return result
            try:
                await journal.flush(types.SimpleNamespace(command=lost_command),lost)
                raise AssertionError('expected lost publish response')
            except ConnectionError:
                pass
            await adapter.TerminalJournal(settings).flush(redis,lost)
            raw=await redis.command('XRANGE',adapter.events_key(settings),'-','+')
            assert sum(json.loads(item[1][item[1].index('payload')+1]).get('eventId')=='lost-reply' for item in raw)==1
            print(json.dumps({'phase':'recover','runtime':'hermes','processRestart':True,'redisAOFRestart':True,'pendingRecovered':True,'stableAttempt':True,'duplicateSuppressed':True,'lostPublishResponse':True,'businessCalls':1}))
    finally:
        redis.close()

asyncio.run(main())
