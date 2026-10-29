import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { withManagedProxyPolicy, withManagedStartupPolicy, synchronizeManagedSharedAuth } from './managed_proxy_policy.mjs';

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'proxy-policy-'));
try {
  const file = path.join(dir, 'policy.json');
  const env = { CLAWMANAGER_PROXY_POLICY_FILE: file };
  const now = 100000;
  const config = { gateway: { trustedProxies: ['10.244.0.0/16'], allowRealIpFallback: true, auth: { mode: 'token' } }, agents: { keep: true } };
  assert.equal(withManagedProxyPolicy(config, {}, now), config);
  assert.deepEqual(withManagedProxyPolicy(config, {CLAWMANAGER_NETWORK_ISOLATED:'1'}, now).gateway.trustedProxies, [], 'private network must deny when policy path is missing');
  assert.throws(() => withManagedStartupPolicy(config, {CLAWMANAGER_NETWORK_ISOLATED:'1'}, now), /PROXY_POLICY_NOT_READY/);
  const verify = expected => {
    const result = withManagedProxyPolicy(config, env, now);
    assert.deepEqual(result.gateway.trustedProxies, expected);
    assert.equal(result.gateway.allowRealIpFallback, false);
    assert.deepEqual(result.gateway.auth, config.gateway.auth);
    assert.equal(result.agents, config.agents);
    assert.deepEqual(config.gateway.trustedProxies, ['10.244.0.0/16']);
  };
  verify([]);
  assert.throws(() => withManagedStartupPolicy({}, env, now), /PROXY_POLICY_NOT_READY/);
  assert.equal(withManagedStartupPolicy(config, {}, now), config, 'standalone/Pro is unchanged');
  const publish = (proxies, expires = now + 60000) => fs.writeFileSync(file, JSON.stringify({ contract: 1, proxies, expires_at_ms: expires }));
  publish(['10.244.166.131', '2001:db8::1']); verify(['10.244.166.131', '2001:db8::1']);
  const isolatedEnv = {...env, CLAWMANAGER_NETWORK_ISOLATED: '1'};
  assert.deepEqual(withManagedProxyPolicy(config, isolatedEnv, now).gateway.trustedProxies, ['127.0.0.1', '::1']);
  const relayConfig = {...config, gateway: {...config.gateway, auth: {mode:'trusted-proxy', trustedProxy: {userHeader:'x-user', allowUsers:['instance-1']}}}};
  const relay = withManagedProxyPolicy(relayConfig, isolatedEnv, now);
  assert.equal(relay.gateway.auth.trustedProxy.allowLoopback, true, 'private authenticated ingress relay must be admitted by native auth');
  assert.equal(relayConfig.gateway.auth.trustedProxy.allowLoopback, undefined, 'overlay must not mutate persisted config');
  assert.equal(withManagedProxyPolicy(relayConfig, env, now).gateway.auth.trustedProxy.allowLoopback, undefined, 'legacy shared NET keeps native same-host rejection');
  assert.deepEqual(relay.gateway.auth.trustedProxy.allowUsers, ['instance-1']);
  assert.equal(withManagedProxyPolicy(relayConfig, isolatedEnv, now + 60000).gateway.auth.trustedProxy.allowLoopback, false, 'expired lease removes same-host admission');
  assert.notEqual(withManagedProxyPolicy(relayConfig, env, now), relay, 'relay and legacy snapshots must never share auth overlays');
  assert.deepEqual(withManagedProxyPolicy(config, env, now).gateway.trustedProxies, ['10.244.166.131', '2001:db8::1']);
  assert.deepEqual(withManagedProxyPolicy(config, isolatedEnv, now + 60000).gateway.trustedProxies, []);
  assert.throws(() => withManagedStartupPolicy(config, isolatedEnv, now + 60000), /PROXY_POLICY_NOT_READY/);
  const first = withManagedProxyPolicy(config, env, now);
  assert.equal(withManagedProxyPolicy(config, env, now), first, 'unchanged managed config must retain native snapshot identity');
  publish(['2001:db8::1', '10.244.166.131', '10.244.166.131'], now + 70000);
  assert.equal(withManagedProxyPolicy(config, env, now), first, 'renewal, ordering and duplicates must not replace the snapshot');
  const changedSource = { ...config, agents: { keep: false } };
  const changed = withManagedProxyPolicy(changedSource, env, now);
  assert.notEqual(changed, first, 'a new native config generation must replace the overlay');
  assert.equal(changed.agents, changedSource.agents);
  publish(['10.244.236.50']); verify(['10.244.236.50']);
  const rotated = withManagedProxyPolicy(config, env, now);
  assert.notEqual(rotated, first, 'a changed proxy set must replace the overlay');
  assert.equal(withManagedProxyPolicy(config, env, now), rotated);
  const expired = withManagedProxyPolicy(config, env, now + 60000);
  assert.notEqual(expired, rotated, 'expiry must invalidate a cached authorized snapshot');
  assert.deepEqual(expired.gateway.trustedProxies, []);
  assert.equal(withManagedProxyPolicy(config, env, now + 60001), expired, 'repeated fail-closed reads retain identity');
  assert.notEqual(withManagedProxyPolicy(config, env, now), expired, 'a valid lease must recover from fail-closed');
  assert.deepEqual(withManagedStartupPolicy({}, env, now).gateway.trustedProxies, ['10.244.236.50']);
  publish(['10.244.0.0/16']); verify([]);
  publish(['0.0.0.0/0']); verify([]);
  publish(['10.244.166.131'], now); verify([]);
  publish(['10.244.166.131'], now + 90001); verify([]);
  publish(['10.244.166.131']);
  assert.deepEqual(withManagedProxyPolicy(config, {...env, CLAWMANAGER_PROXY_POLICY_NAMESPACE: 'other-tenant'}, now).gateway.trustedProxies, [], 'cross-tenant policy must be rejected');
  fs.writeFileSync(file, '{broken'); verify([]);
  const state = { current: 'A', required: null };
  let disconnected = 0, resolved = 0, allowReplace = true;
  const params = {
    state, clients: new Set(),
    capture: value => ({ ...value }),
    resolveGeneration: () => { resolved++; return 'B'; },
    replaceOwned: (value, owner, replacement) => {
      if (!allowReplace || value.current !== owner.current || value.required !== owner.required) return false;
      Object.assign(value, replacement); return true;
    },
    disconnect: ({expectedGeneration}) => { assert.equal(expectedGeneration, 'B'); disconnected++; },
  };
  synchronizeManagedSharedAuth(params, {});
  assert.equal(resolved, 0, 'unmanaged auth must remain native');
  state.required = 'config-transaction';
  synchronizeManagedSharedAuth(params, env);
  assert.equal(resolved, 0, 'must not overwrite the native transaction fence');
  state.required = null;
  allowReplace = false;
  synchronizeManagedSharedAuth(params, env);
  assert.equal(state.current, 'A'); assert.equal(disconnected, 0, 'lost ownership must not disconnect clients');
  allowReplace = true;
  synchronizeManagedSharedAuth(params, env);
  assert.equal(state.current, 'B'); assert.equal(disconnected, 1);
  synchronizeManagedSharedAuth(params, env);
  assert.equal(disconnected, 1, 'unchanged generation/renewal must not rotate connections');
  console.log('PASS: scoped live proxy overlay, rotation, expiration, malformed policy, IPv6 and auth preservation');
} finally {
  fs.rmSync(dir, { recursive: true, force: true });
}
