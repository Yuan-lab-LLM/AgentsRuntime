"""Run inside a candidate image. Uses temporary homes and a local model fixture only."""
import base64
import html
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PATCH = '/usr/local/share/deepseek-harness/managed.cordis.patch.yml'
REQUESTS = []


class ModelFixture(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        REQUESTS.append((self.path, self.headers.get('Authorization'), body))
        self.send_response(200)
        if body.get('stream'):
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            tool_test = any('READ_FIXTURE' in str(message.get('content', '')) for message in body['messages'])
            tool_result = any(message.get('role') == 'tool' for message in body['messages'])
            if tool_test and not tool_result:
                deltas = [({'role': 'assistant', 'tool_calls': [{'index': 0, 'id': 'fixture-read', 'type': 'function',
                            'function': {'name': 'read', 'arguments': '{"file_path":"fixture.txt"}'}}]}, None), ({}, 'tool_calls')]
            else:
                deltas = [({'role': 'assistant', 'content': 'pong'}, None), ({}, 'stop')]
            for delta, finish in deltas:
                chunk = {'id': 'fixture', 'object': 'chat.completion.chunk', 'created': 1,
                         'model': body['model'], 'choices': [{'index': 0, 'delta': delta, 'finish_reason': finish}]}
                self.wfile.write(('data: ' + json.dumps(chunk) + '\n\n').encode())
            self.wfile.write(b'data: [DONE]\n\n')
        else:
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps({'id': 'fixture', 'model': body['model'], 'choices': [
                {'index': 0, 'message': {'role': 'assistant', 'content': 'pong'}, 'finish_reason': 'stop'}],
                'usage': {'prompt_tokens': 10, 'completion_tokens': 1, 'total_tokens': 11}}).encode())


def request(url, headers=None):
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read()


def rpc(url, method, args, headers):
    parameters = {} if method == 'session/modelCatalog' else {
        '_request' if method == 'session/list' else 'request': args}
    message = {'type': 'client-request', 'rpcId': str(uuid.uuid4()),
               'method': method, 'payload': {'args': parameters}}
    req = urllib.request.Request(url + '/api/' + method, data=json.dumps(message).encode(),
                                 headers={**headers, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=60) as response:
        result = json.load(response)['result']
    assert result['ok'], (method, result)
    return result['value']


def check_web_model(url, headers, root):
    catalog = rpc(url, 'session/modelCatalog', {}, headers)
    routes = {(group['id'], model['id']) for group in catalog['groups'] for model in group['models']}
    assert ('auto', 'auto') in routes, catalog
    assert ('managed', 'demo') in routes, catalog
    assert not catalog['failures'], catalog
    session = rpc(url, 'session/create', {'cwd': str(root), 'agentPreset': 'standard'}, headers)
    session_id = session['sessionId']
    rpc(url, 'session/prompt', {'sessionId': session_id, 'requestId': str(uuid.uuid4()),
        'mode': 'queue', 'content': [{'type': 'text', 'text': 'WEB_MODEL_FIXTURE: say hello.'}]}, headers)
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        baseline = rpc(url, 'session/projections', {'sessionId': session_id}, headers)
        page = rpc(url, 'session/page', {'address': {'kind': 'session', 'sessionId': session_id},
                   'throughSeq': baseline['asOfSeq']}, headers)
        sessions = rpc(url, 'session/list', {}, headers)['items']
        if 'pong' in json.dumps(page['records']) and any(
                item['sessionId'] == session_id and not item['running'] for item in sessions):
            break
        time.sleep(0.5)
    else:
        raise AssertionError('Web session did not persist a completed model reply: ' + session_id)
    assert any(body['model'] == 'auto' and auth == 'Bearer fixture-model' and
               any('WEB_MODEL_FIXTURE' in str(m.get('content', '')) for m in body['messages'])
               for _, auth, body in REQUESTS), 'Web prompt did not reach managed model endpoint'


def stop(process):
    if process.poll() is None:
        children = Path('/proc/%d/task/%d/children' % (process.pid, process.pid)).read_text().split()
        # Signal only the proxy, just as the runtime supervisor does. It must
        # cancel and reap DSH itself instead of relying on a container shutdown.
        process.terminate()
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=10)
            raise AssertionError('proxy did not stop after SIGTERM')
        remaining = [pid for pid in children if Path('/proc/' + pid).exists()]
        if remaining:
            os.killpg(process.pid, signal.SIGKILL)
            raise AssertionError('proxy left child processes running: ' + str(remaining))


def check_web(mode, env, root):
    script = '/usr/local/bin/start-deepseek-harness-' + ('gateway' if mode == 'lite' else 'pro')
    port = 20397 if mode == 'lite' else 3080
    env['CLAWMANAGER_GATEWAY_PORT'] = str(port)
    headers = {'X-ClawManager-Instance-Token': 'fixture-instance'} if mode == 'lite' else {}
    url = 'http://127.0.0.1:%d' % port
    for attempt in range(2):
        log_path = root / ('%s-%d.log' % (mode, attempt))
        with log_path.open('w') as log:
            process = subprocess.Popen([script], env=env, cwd=root, stdout=log, stderr=log, start_new_session=True)
            try:
                deadline = time.monotonic() + 180
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise AssertionError('startup exited: ' + log_path.read_text()[-6000:])
                    try:
                        status, _, document = request(url + '/', headers)
                        if status == 200:
                            break
                    except (OSError, urllib.error.URLError):
                        pass
                    time.sleep(1)
                else:
                    raise AssertionError('startup timeout: ' + log_path.read_text()[-6000:])
                assert b'<html' in document.lower(), document[:200]
                print(mode, attempt, 'authenticated index ready', flush=True)
                check_web_model(url, headers, root)
                print(mode, attempt, 'Web model catalog and completed reply OK', flush=True)
                status, response_headers, _ = request(url + '/', headers)
                assert 'dsh-auth-' not in str(response_headers)
                if mode == 'lite':
                    assert request(url + '/')[0] == 401
                    assert request(url + '/', {'Authorization': 'Bearer wrong'})[0] == 401
                else:
                    assert request(url + '/', {'Origin': 'https://untrusted.example'})[0] == 401
                for asset in re.findall(r'(?:src|href)="([^"\s]+\.(?:js|css)(?:\?[^"\s]*)?)"', document.decode()):
                    if not asset.startswith(('http:', 'https:', '//')):
                        assert request(url + '/' + asset.lstrip('./'), headers)[0] == 200, asset
                # Use the published revision and IDs, not an invented batch URL.
                # DSH's significant leading '?' must reach the real module loader.
                batches = set(re.findall(r'plugins/\?\?[^"\s<>]+', document.decode()))
                assert batches, 'no plugin batches in the actual index'
                for batch in batches:
                    status, _, _ = request(url + '/' + html.unescape(batch), headers)
                    assert status == 200, ('plugin batch', status, batch)
                sock = socket.create_connection(('127.0.0.1', port), timeout=5)
                try:
                    lines = ['GET /api/remote.mux HTTP/1.1', 'Host: 127.0.0.1:%d' % port,
                             'Upgrade: websocket', 'Connection: Upgrade', 'Sec-WebSocket-Version: 13',
                             'Sec-WebSocket-Key: ' + base64.b64encode(os.urandom(16)).decode(),
                             'Origin: ' + url] + ['%s: %s' % item for item in headers.items()]
                    sock.sendall(('\r\n'.join(lines) + '\r\n\r\n').encode())
                    handshake = sock.recv(4096)
                    assert b'101 Switching Protocols' in handshake, handshake[:300]
                finally:
                    sock.close()
                skill = Path(env['DSH_HOME']) / 'skills' / 'managed-fixture' / 'SKILL.md'
                skill.parent.mkdir(parents=True, exist_ok=True)
                skill.write_text('---\nname: managed-fixture\ndescription: Fixture skill\n---\nFixture body\n')
                print(mode, 'restart' if attempt else 'fresh', 'HTTP/assets/plugins/WebSocket/auth OK', flush=True)
            finally:
                stop(process)
        text = log_path.read_text()
        assert '?token=' not in text, 'launch token leaked to supervisor log'


def main():
    fixture = ThreadingHTTPServer(('127.0.0.1', 0), ModelFixture)
    threading.Thread(target=fixture.serve_forever, daemon=True).start()
    with tempfile.TemporaryDirectory(prefix='dsh-managed-smoke-') as directory:
        root = Path(directory)
        env = dict(os.environ, HOME=str(root), DSH_HOME=str(root / '.dsh'),
                   DSH_TELEMETRY_DISABLED='1', CLAWMANAGER_INSTANCE_TOKEN='fixture-instance',
                   CLAWMANAGER_LLM_API_KEY='fixture-model', CLAWMANAGER_LLM_MODEL='["auto"]',
                   CLAWMANAGER_LLM_BASE_URL='http://127.0.0.1:%d/v1' % fixture.server_port,
                   CLAWMANAGER_LLM_CONTEXT_WINDOW='64000', CLAWMANAGER_LLM_CONTEXT_WINDOWS='{"auto":64000}')
        web_env = dict(env, CLAWMANAGER_LLM_PROVIDER_MODELS='["auto/auto","managed/demo"]',
                       CLAWMANAGER_LLM_MODEL='["auto","demo"]', CLAWMANAGER_LLM_CONTEXT_WINDOW='0',
                       CLAWMANAGER_LLM_CONTEXT_WINDOWS='{"auto":0,"demo":32000}')
        for mode in sys.argv[1:] or ['lite', 'pro']:
            assert mode in ['lite', 'pro'], 'mode must be lite or pro'
            check_web(mode, web_env.copy(), root)
        canonical = subprocess.check_output(['/usr/local/bin/clawmanager-agent', 'llm-config', '--format', 'canonical',
                                             '--fallback-provider', 'clawmanager', '--default-model', 'auto'], env=env, text=True)
        env['CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON'] = canonical
        result = subprocess.run(['dsh', 'headless', '--patch', PATCH, '--json', 'Reply with pong.'],
                                cwd=root, env=env, text=True, capture_output=True, timeout=180)
        assert result.returncode == 0, result.stderr[-6000:]
        assert 'pong' in result.stdout, result.stdout
        assert REQUESTS, 'model gateway received no requests'
        assert any('managed-fixture' in json.dumps(body) for _, _, body in REQUESTS), 'installed skill was not discovered'
        (root / 'fixture.txt').write_text('verified-tool-read-sentinel\n')
        result = subprocess.run(['dsh', 'headless', '--patch', PATCH, '--json', 'READ_FIXTURE and reply with pong.'],
                                cwd=root, env=env, text=True, capture_output=True, timeout=180)
        assert result.returncode == 0 and 'pong' in result.stdout, result.stderr[-3000:]
        assert any(message.get('role') == 'tool' and 'verified-tool-read-sentinel' in str(message.get('content'))
                   for _, _, body in REQUESTS for message in body['messages']), 'read tool did not execute'
        for path, authorization, body in REQUESTS:
            assert path == '/v1/chat/completions', path
            assert authorization == 'Bearer fixture-model'
            assert body['model'] == 'auto'
        print('gateway streaming/model credentials/read tool/session persistence OK; tools:',
              [tool['function']['name'] for tool in REQUESTS[0][2].get('tools', [])], flush=True)
    fixture.shutdown()


if __name__ == '__main__':
    main()
