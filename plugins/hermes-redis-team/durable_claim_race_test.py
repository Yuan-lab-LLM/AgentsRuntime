"""Actual Hermes consumer/real Redis/journal admission race, no model calls."""
import asyncio
import json
import os
import tempfile
import types
import uuid
from unittest import mock
from test_adapter_contract import adapter

async def run(ordering):
    with tempfile.TemporaryDirectory() as root:
        cfg=adapter.RedisTeamSettings(enabled=True,auto_run=True,redis_url=os.environ['HERMES_REDIS_TEST_URL'],team_id='claim-'+ordering+'-'+uuid.uuid4().hex,member_id='worker',shared_dir=root)
        redis=adapter.AsyncRedisClient(cfg.redis_url);await redis.connect()
        assert await redis.command('GET','cm:test:runtime-durable:isolated')=='runtime-durable-20261118'
        e={'teamId':cfg.team_id,'to':'worker','from':'leader','taskId':'root','rootTaskId':'root','assignmentId':'work','revision':1,'messageId':'same','idempotencyKey':'same','text':'Execute once'}
        await redis.command('XGROUP','CREATE',adapter.inbox_key(cfg),cfg.consumer_group,'0','MKSTREAM')
        await redis.command('XADD',adapter.inbox_key(cfg),'*','payload',json.dumps(e))
        raw=await redis.command('XREADGROUP','GROUP',cfg.consumer_group,cfg.member_id,'STREAMS',adapter.inbox_key(cfg),'>')
        e=adapter._parse_stream_response(raw)[0]
        j=adapter.TerminalJournal(cfg);j.admit(e);j.context(e,'queued')
        serial=context=0;queued=False;lose_ack=True;original=redis.command
        async def context_claim():
            nonlocal context
            if j.context(e,'submitting'):
                context+=1
                j.context(e,'submitted')
        async def command(*args):
            nonlocal queued
            if args[0]=='XACK' and lose_ack:raise ConnectionError('lost final ACK')
            result=await original(*args)
            if args[0]=='XADD' and ordering=='context-first' and not queued:
                queued=True
                await context_claim()  # Wins after actual admission snapshot / network await.
            return result
        redis.command=command
        with mock.patch.object(adapter,'load_settings',return_value=cfg):
            instance=adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
        instance._redis=redis
        async def dispatch(*args,**kwargs):
            nonlocal serial
            serial+=1
            if ordering=='serial-first':await context_claim()
        instance._dispatch_envelope=dispatch
        try:
            await instance._handle_redis_message(e)
        except ConnectionError:
            assert ordering=='serial-first'
        assert serial+context==1,(ordering,serial,context)
        assert context==(ordering=='context-first')
        assert (await original('XPENDING',adapter.inbox_key(cfg),cfg.consumer_group))[0]==1
        await original('DEL',adapter._processed_message_key(cfg,e['idempotencyKey']))
        lose_ack=False
        with mock.patch.object(adapter,'load_settings',return_value=cfg):
            restarted=adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
        restarted._redis=redis;restarted._dispatch_envelope=dispatch
        await restarted._handle_redis_message(e)
        assert (await original('XPENDING',adapter.inbox_key(cfg),cfg.consumer_group))[0]==0
        assert serial+context==1 and j.read(e)['state']=='accepted'
        redis.close()
        print(json.dumps({'runtime':'hermes','ordering':ordering,'serialDispatches':serial,'contextSubmissions':context,'lostACK':True,'lostProcessedMarker':True,'restartExecutions':0}))
asyncio.run(run('context-first'));asyncio.run(run('serial-first'))
