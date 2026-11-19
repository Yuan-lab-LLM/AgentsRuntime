import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {mkdtemp, mkdir, readFile, writeFile, stat} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';

const shell = process.platform === 'win32' ? 'C:\\Program Files\\Git\\bin\\bash.exe' : 'bash';
const shellPath = (value) => process.platform === 'win32'
  ? value.replace(/^([A-Za-z]):/, (_, drive) => '/' + drive.toLowerCase()).replaceAll('\\', '/')
  : value;

const initSource = await readFile(new URL('../rootfs/custom-cont-init.d/99-opencode-config', import.meta.url), 'utf8');
const initRoot = await mkdtemp(join(tmpdir(), 'opencode-init-test-'));
const initConfig = shellPath(join(initRoot, 'config'));
const initScript = join(initRoot, '99-opencode-config');
await writeFile(initScript, initSource
  .replaceAll('/config', initConfig)
  .replace('/usr/local/bin/opencode-apply-runtime-config', 'true')
  .replaceAll('id abc', 'false'));
const initEnv = {
  ...process.env,
  HOME: initConfig,
  OPENCODE_CONFIG_DIR: initConfig + '/.opencode',
  CLAWMANAGER_PROJECT_PATH: initConfig + '/workspace',
  CLAWMANAGER_AGENT_PERSISTENT_DIR: initConfig,
  CLAWMANAGER_DEFAULT_PROJECT_PATH: initConfig + '/workspace/project',
  CLAWMANAGER_RUNTIME_TYPE: 'opencode',
};
const initResult = spawnSync(shell, [shellPath(initScript)], {encoding: 'utf8', env: initEnv});
assert.equal(initResult.status, 0, initResult.stderr);
await assert.rejects(stat(join(initRoot, 'config', 'workspace', 'project')), {code: 'ENOENT'});
await assert.rejects(stat(join(initRoot, 'config', 'workspace', '.git')), {code: 'ENOENT'});
console.log('PASS: Lite container initialization does not create a default project or Git repository.');

const proRoot = await mkdtemp(join(tmpdir(), 'opencode-pro-init-test-'));
const proConfig = shellPath(join(proRoot, 'config'));
const proScript = join(proRoot, '99-opencode-config');
await writeFile(proScript, initSource
  .replaceAll('/config', proConfig)
  .replace('/usr/local/bin/opencode-apply-runtime-config', 'true')
  .replaceAll('id abc', 'false'));
const proResult = spawnSync(shell, [shellPath(proScript)], {
  encoding: 'utf8',
  env: {
    ...initEnv,
    HOME: proConfig,
    OPENCODE_CONFIG_DIR: proConfig + '/.opencode',
    CLAWMANAGER_PROJECT_PATH: proConfig + '/workspace',
    CLAWMANAGER_AGENT_PERSISTENT_DIR: proConfig,
    CLAWMANAGER_DEFAULT_PROJECT_PATH: proConfig + '/workspace/project',
    CLAWMANAGER_RUNTIME_TYPE: 'desktop',
  },
});
assert.equal(proResult.status, 0, proResult.stderr);
assert.equal((await stat(join(proRoot, 'config', 'workspace', 'project'))).isDirectory(), true);
console.log('PASS: Pro container initialization retains its default project.');

const source = await readFile(new URL('../rootfs/usr/local/bin/start-opencode-web', import.meta.url), 'utf8');
const tail = 'exec /usr/local/bin/opencode serve --hostname "${host}" --port "${port}"';
assert.ok(source.includes(tail));
const root = await mkdtemp(join(tmpdir(), 'opencode-start-test-'));
const workspace = join(root, 'workspace');
await mkdir(workspace);
const script = join(root, 'start-opencode-web');
await writeFile(script, source.replace(tail, 'printf "PWD=%s\\nPROJECT=%s\\nDEFAULT=%s\\n" "$PWD" "$CLAWMANAGER_PROJECT_PATH" "${CLAWMANAGER_DEFAULT_PROJECT_PATH:-}"'));
const result = spawnSync(shell, [shellPath(script)], {
  encoding: 'utf8',
  env: {
    ...process.env,
    HOME: shellPath(root),
    OPENCODE_CONFIG_DIR: shellPath(join(root, 'config')),
    CLAWMANAGER_GATEWAY_PORT: '20042',
    OPENCODE_SERVER_PASSWORD: 'test-only',
    CLAWMANAGER_RUNTIME_TYPE: 'opencode',
    CLAWMANAGER_WORKSPACE_PATH: shellPath(workspace),
    CLAWMANAGER_DEFAULT_PROJECT_PATH: shellPath(join(workspace, 'project')),
  },
});
assert.equal(result.status, 0, result.stderr);
assert.match(result.stdout, /DEFAULT=\s*$/m);
assert.ok(result.stdout.includes('PROJECT=' + shellPath(workspace)));
await assert.rejects(stat(join(workspace, 'project')), {code: 'ENOENT'});
await assert.rejects(stat(join(workspace, '.git')), {code: 'ENOENT'});
console.log('PASS: Lite startup leaves the workspace free of default projects and Git metadata.');
