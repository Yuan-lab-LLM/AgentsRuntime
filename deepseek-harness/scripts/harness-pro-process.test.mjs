import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';

test('Harness Pro terminates only its verified private Linux process session', { skip: process.platform !== 'linux' }, () => {
  for (const matching of [true, false]) {
    const code = [
      'import {terminateHarnessRuntime} from ' + JSON.stringify(new URL('./managed-model-reference.mjs', import.meta.url).href) + ';',
      'process.env.CLAWMANAGER_HARNESS_FAIL_STOP_PGID=String(process.pid+' + (matching ? '0' : '1') + ');',
      'terminateHarnessRuntime();',
    ].join('\n');
    // detached creates a new session: the parent test runner is never in the
    // group being signalled. This test does not touch any running instance.
    assert.throws(() => execFileSync(process.execPath, ['--input-type=module','-e',code],
      { detached:true, timeout:5000 }), error => matching ? error.signal === 'SIGKILL' : error.status === 78);
  }
});
