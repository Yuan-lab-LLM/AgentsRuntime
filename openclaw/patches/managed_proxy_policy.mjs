// Managed Lite only. The runtime agent owns this file outside user workspaces.
// A missing/expired/malformed policy fails closed, never falling back to a broad
// trustedProxies value from an old user configuration. Standalone/Pro is unchanged.
import fs from 'node:fs';
import { isIP } from 'node:net';

// Native 8.1 readers use config object identity as a publication fence. Keep
// one effective snapshot per immutable source config, not one clone per read.
// Always validate the file first: this caches identity, never authorization.
const effectiveSnapshots = new WeakMap();

export function withManagedProxyPolicy(config, env = process.env, now = Date.now()) {
  const file = env.CLAWMANAGER_PROXY_POLICY_FILE;
  if (!file && env.CLAWMANAGER_NETWORK_ISOLATED !== '1') return config;
  let proxies = [];
  try {
    const raw = fs.readFileSync(file, 'utf8');
    if (raw.length > 16384) throw new Error('policy too large');
    const policy = JSON.parse(raw);
    if (policy.contract !== 1 || (env.CLAWMANAGER_PROXY_POLICY_NAMESPACE && policy.namespace !== env.CLAWMANAGER_PROXY_POLICY_NAMESPACE) || !Number.isSafeInteger(policy.expires_at_ms) ||
        policy.expires_at_ms <= now || policy.expires_at_ms > now + 90000 ||
        !Array.isArray(policy.proxies) || policy.proxies.length === 0 || policy.proxies.length > 128 ||
        policy.proxies.some(ip => typeof ip !== 'string' || !isIP(ip))) {
      throw new Error('invalid policy');
    }
    proxies = env.CLAWMANAGER_NETWORK_ISOLATED === '1' ? ['127.0.0.1', '::1'] : [...new Set(policy.proxies)].sort();
  } catch {
    // Authentication and attribution stay enabled even when discovery is down.
  }
  const namespace = env.CLAWMANAGER_PROXY_POLICY_NAMESPACE ?? '';
  const isolated = env.CLAWMANAGER_NETWORK_ISOLATED === '1';
  const proxyKey = JSON.stringify([isolated, proxies]);
  const cacheable = config !== null && typeof config === 'object';
  const previous = cacheable ? effectiveSnapshots.get(config) : undefined;
  if (previous && previous.file === file && previous.namespace === namespace && previous.proxyKey === proxyKey) {
    return previous.config;
  }
  const gateway = { ...config?.gateway, trustedProxies: proxies, allowRealIpFallback: false };
  if (isolated) {
    // Protected ingress terminates only after the parent verified the current
    // instance capability, then relays through this instance's private NET.
    // 10.2 independently refuses loopback trusted-proxy authentication unless
    // explicitly admitted. This overlay never changes the shared-NET path or
    // persists the exception, and expires with the parent-owned policy lease.
    gateway.auth = { ...gateway.auth, trustedProxy: { ...gateway.auth?.trustedProxy, allowLoopback: proxies.length > 0 } };
  }
  const effective = { ...config, gateway };
  if (cacheable) effectiveSnapshots.set(config, { file, namespace, proxyKey, config: effective });
  return effective;
}

// Startup and live requests must share exactly the same policy validation.
// Never persist ephemeral proxy addresses in the user's configuration.
export function withManagedStartupPolicy(config, env = process.env, now = Date.now()) {
  const result = withManagedProxyPolicy(config, env, now);
  if ((env.CLAWMANAGER_PROXY_POLICY_FILE || env.CLAWMANAGER_NETWORK_ISOLATED === '1') && result.gateway.trustedProxies.length === 0) {
    throw new Error('PROXY_POLICY_NOT_READY: a valid managed proxy policy is required before Gateway startup');
  }
  return result;
}

// The external policy is part of trusted-proxy shared authentication, not just
// request attribution. Keep the native generation owner/fence authoritative:
// never replace an in-flight config transaction's explicit required marker.
export function synchronizeManagedSharedAuth(params, env = process.env) {
  if (!env.CLAWMANAGER_PROXY_POLICY_FILE || params.state.required !== null) return;
  const ownership = params.capture(params.state);
  const generation = params.resolveGeneration();
  if (generation === params.state.current) return;
  if (params.replaceOwned(params.state, ownership, { current: generation, required: null })) {
    params.disconnect({ state: params.state, clients: params.clients, expectedGeneration: generation });
  }
}
