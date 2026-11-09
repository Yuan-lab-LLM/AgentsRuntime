"""Actual Agent + serve + Team plugin acceptance; disposable Redis and model stub.

Invoked by smoke_lite_agent when HERMES_TEAM_TEST_REDIS_URL is set. The
coordinator below only acknowledges explicit proposals: it is not CM business
state-machine evidence. No external models, tenant data or credentials are used.
"""
import asyncio
import http.cookiejar
import json
import os
from pathlib import Path
import threading
import time
import urllib.request

from plugins.platforms.redis_team.adapter import AsyncRedisClient
from smoke_lite_desktop_rpc import ModelStub, RPC
from websockets.asyncio.client import connect


class FixtureRedis:
    """Use the shipped RESP client; acceptance needs no extra image packages."""
    def __init__(self, url):
        self.loop = asyncio.new_event_loop()
        self.thread = threading.Thread(target=self.loop.run_forever, daemon=True)
        self.thread.start()
        self.client = AsyncRedisClient(url)
        asyncio.run_coroutine_threadsafe(self.client.connect(), self.loop).result(5)

    def command(self, *args):
        return asyncio.run_coroutine_threadsafe(self.client.command(*args), self.loop).result(10)

    def xread(self, streams, block, count):
        rows = self.command('XREAD', 'BLOCK', block, 'COUNT', count, 'STREAMS', *streams.keys(), *streams.values())
        return [(name, [(rid, dict(zip(fields[::2], fields[1::2]))) for rid, fields in messages]) for name, messages in (rows or [])]

    def set(self, key, value, ex):
        return self.command('SET', key, value, 'EX', ex)

    def xadd(self, stream, fields):
        return self.command('XADD', stream, '*', *[item for pair in fields.items() for item in pair])

    def close(self):
        self.loop.call_soon_threadsafe(self.client.close)
        self.loop.call_soon_threadsafe(self.loop.stop)
        self.thread.join(timeout=3)
        self.loop.close()


def exercise_team(control, await_running, workspaces, keys):
    db = FixtureRedis(os.environ['HERMES_TEAM_TEST_REDIS_URL'])
    assert db.command('PING') == 'PONG'
    team = str(time.time_ns())
    stubs, requests, clients, turn_counts = [], [], [], []
    origin = 'http://clawmanager-backend.smoke.svc:19091'
    shared = workspaces / 'teams' / 'user-7' / ('team-' + team + '-shared')
    shared.mkdir(parents=True, mode=0o2777)
    shared.chmod(0o2777)
    members = ['leader', 'developer', 'reviewer']
    (shared / 'team.json').write_text(json.dumps({'members': [{'memberId': m, 'role': m} for m in members]}))
    stop = threading.Event()
    completions = set()
    restart_receipt = []

    def coordinator():
        cursor = '0-0'
        while not stop.is_set():
            for _, rows in db.xread({'claw:team:' + team + ':events': cursor}, block=200, count=50):
                for cursor, fields in rows:
                    event = json.loads(fields.get('payload', '{}'))
                    payload = event.get('payload', event)
                    if event.get('event') != 'completion_proposed':
                        continue
                    cid, attempt = payload.get('completionId'), payload.get('attemptId')
                    if not cid or not attempt:
                        continue
                    import re
                    def key(value):
                        return re.sub(r'[^A-Za-z0-9_:.-]', '-', value)
                    prefix = 'claw:team:' + team
                    ack_key = prefix + ':completion-ack:' + key(cid) + ':' + key(attempt)
                    def accept(cid=cid, attempt=attempt, ack_key=ack_key):
                        if not stop.is_set():
                            db.set(ack_key, json.dumps({'decision': 'accepted', 'completionId': cid, 'attemptId': attempt}), ex=120)
                            completions.add(cid)
                    if event.get('memberId') == 'leader':
                        accept()
                    else:
                        if event.get('memberId') == 'reviewer':
                            db.set(ack_key, json.dumps({'decision': 'deferred', 'completionId': cid, 'attemptId': attempt}), ex=120)
                            restart_receipt.append(accept)
                            continue  # Release only after the old Gateway is gone.
                        # Exercise submitted (no immediate ACK) and deferred;
                        # both must yield and later accept without a model call.
                        timer = threading.Timer(8, accept)
                        timer.daemon = True
                        timer.start()

    worker = threading.Thread(target=coordinator, daemon=True)
    worker.start()
    try:
        for offset, member in enumerate(members):
            iid, uid = 21 + offset, 11001 + offset
            workspace = workspaces / 'hermes/user-7' / ('instance-' + str(iid))
            artifact = shared / (member + '.md')
            artifact.write_text('Deterministic Team acceptance delivery: ' + member)
            artifact.chmod(0o664)
            count = [0]
            turn_counts.append(count)

            def reply(payload, count=count, artifact=artifact):
                names = {x.get('function', {}).get('name') for x in payload.get('tools', [])}
                if 'team_complete_task' not in names and not ('tool_call' in names and 'team_complete_task' in json.dumps(payload.get('tools', []))):
                    return None
                count[0] += 1
                if count[0] > 1:
                    return None  # Record a regression without causing model HTTP retries.
                args = {'status': 'succeeded', 'summary': 'Team acceptance done', 'resultMarkdown': 'Team acceptance done', 'artifactRefs': ['/team/' + artifact.name]}
                if 'team_complete_task' in names:
                    return {'name': 'team_complete_task', 'arguments': json.dumps(args)}
                return {'name': 'tool_call', 'arguments': json.dumps({'name': 'team_complete_task', 'arguments': args})}

            stub = ModelStub(workspace, keys['llm'], tool_reply=reply)
            stubs.append(stub)
            port = 20010 + offset
            req = {'instance_id': iid, 'user_id': 7, 'agent_type': 'hermes', 'workspace_path': str(workspace),
                   'gateway_port': port, 'uid': uid, 'gid': 12000, 'generation': 1,
                   'environment': {'CLAWMANAGER_INSTANCE_TOKEN': keys['instance'] + member,
                       'CLAWMANAGER_LLM_API_KEY': keys['llm'], 'CLAWMANAGER_LLM_BASE_URL': stub.url.replace('127.0.0.1', 'clawmanager-backend.smoke.svc'),
                       'CLAWMANAGER_LLM_MODEL': 'smoke-model', 'CLAWMANAGER_LLM_PROVIDER': 'custom',
                       'CLAWMANAGER_TEAM_ENABLED': 'true', 'CLAWMANAGER_TEAM_ID': team,
                       'CLAWMANAGER_TEAM_MEMBER_ID': member, 'CLAWMANAGER_TEAM_ROLE': member,
                       'CLAWMANAGER_TEAM_REDIS_URL': os.environ['HERMES_TEAM_TEST_REDIS_URL'],
                       'CLAWMANAGER_TEAM_SHARED_DIR': str(shared), 'CLAWMANAGER_TEAM_SHARED_GID': '12000'}}
            started = control('/v1/gateways', req)
            running = await_running(iid, 1)
            assert running['gateway_pid'] > 0
            requests.append((req, started))
            cookies = http.cookiejar.CookieJar()
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(cookies))

            def request(path, body=None, port=port, opener=opener):
                data = None if body is None else json.dumps(body).encode()
                r = urllib.request.Request('http://127.0.0.1:' + str(port) + path, data=data,
                    headers={'Origin': origin, 'Content-Type': 'application/json'})
                with opener.open(r, timeout=10) as response:
                    return json.load(response)

            assert request('/auth/password-login', {'provider': 'basic', 'username': 'clawmanager', 'password': keys['instance'] + member})['ok']
            assert request('/api/health')['ok']
            clients.append((request, port))
            envelope = {'messageId': 'serve-team-' + member, 'teamId': team, 'from': 'ClawManager', 'to': member,
                        'taskId': 'team-' + team + '-task-1', 'rootTaskId': 'team-' + team + '-task-1', 'assignmentId': member + '-work',
                        'revision': 1, 'type': 'task', 'intent': 'task_assignment', 'requiresCompletion': True,
                        'text': 'SMOKE_TEXT Deliver the assigned fixture and call team_complete_task.', 'conversationId': 'team-' + team + '-task-1'}
            db.xadd('claw:team:' + team + ':inbox:' + member, {'payload': json.dumps(envelope)})

        deadline = time.monotonic() + 120
        while (len(completions) < 2 or not restart_receipt) and time.monotonic() < deadline:
            time.sleep(.25)
        assert len(completions) == 2 and restart_receipt, 'three real members did not submit explicit completions'
        time.sleep(2)

        def active(member):
            return json.loads((shared / '.hermes-redis-team' / ('active-' + member + '.json')).read_text())

        assert active('reviewer')['completionDecision'] == 'deferred', 'deferred receipt missing'
        req, started = requests[2]
        control('/v1/gateways/' + started['gateway_id'], method='DELETE')
        restart_receipt[0]()
        req['generation'] = 2
        second = control('/v1/gateways', req)
        requests[2] = req, second
        await_running(req['instance_id'], 2)
        clients[2][0]('/auth/password-login', {'provider': 'basic', 'username': 'clawmanager', 'password': keys['instance'] + 'reviewer'})
        deadline = time.monotonic() + 75
        while not all(active(m).get('terminal') for m in members) and time.monotonic() < deadline:
            time.sleep(.25)
        assert all(active(m).get('terminal') and active(m).get('completionDecision') == 'accepted' for m in members), 'late ACK not reconciled by running/restarted Team consumer'
        assert all(count == [1] for count in turn_counts), 'durable completion continued the model loop'
        for stub in stubs:
            assert stub.tools & {'team_complete_task', 'tool_call'}, 'Team tools missing from real model schema'

        # Verify the actual Web history endpoint can see the persisted Team turn.
        histories = []
        for request, port in clients:
            sessions = request('/api/sessions')
            assert 'redis_team' in json.dumps(sessions), 'Team conversation absent from Web session history'
            rows = sessions if isinstance(sessions, list) else sessions['sessions']
            team_row = next(row for row in rows if row.get('source') == 'redis_team')
            path = '/api/sessions/' + team_row['id'] + '/messages'
            history = request(path)
            assert 'SMOKE_TEXT' in json.dumps(history) and 'team_complete_task' in json.dumps(history), 'Web history missing actual Team request/tool receipt'
            histories.append((path, history))
            async def websocket():
                ticket = request('/api/auth/ws-ticket', {})['ticket']
                async with connect('ws://127.0.0.1:' + str(port) + '/api/ws', origin=origin,
                                   subprotocols=['hermes-gateway-v1', 'hermes-gateway-ticket.' + ticket], proxy=None) as ws:
                    rpc = RPC(ws)
                    assert await rpc.call('ping') is not None
                    rpc.reader.cancel()
            asyncio.run(websocket())
        print('PASS: three real Team consumers + serve authentication/WS + explicit proposals + delayed ACK/restart recovery + Web history', flush=True)
        for index, (req, started) in enumerate(requests):
            control('/v1/gateways/' + started['gateway_id'], method='DELETE')
            req['generation'] += 1
            second = control('/v1/gateways', req)
            await_running(req['instance_id'], req['generation'])
            request = clients[index][0]
            request('/auth/password-login', {'provider': 'basic', 'username': 'clawmanager', 'password': keys['instance'] + members[index]})
            path, history = histories[index]
            assert request(path) == history, 'cold Team restart changed durable Web history'
            control('/v1/gateways/' + second['gateway_id'], method='DELETE')
        print('PASS: Team generation replacement and process cleanup preserve workspace', flush=True)
    except Exception:
        for stub in stubs:
            print('Team model tools: ' + json.dumps(sorted(stub.tools)), flush=True)
        for path in workspaces.rglob('*.log'):
            diagnostic = path.read_text(errors='replace')[-4000:]
            for value in keys.values():
                diagnostic = diagnostic.replace(value, '[REDACTED]')
            print(str(path.relative_to(workspaces)) + '\n' + diagnostic, flush=True)
        raise
    finally:
        stop.set()
        worker.join(timeout=3)
        for stub in stubs:
            stub.close()
        db.close()
