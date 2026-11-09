// Verification only. The private release key is never available in this image.
import { readFileSync, realpathSync } from 'node:fs'
import { createPublicKey, verify } from 'node:crypto'
import { pathToFileURL } from 'node:url'

export function verifyCampaign(trust, envelope) {
  if (trust.schema_version !== 1 || trust.algorithm !== 'Ed25519' ||
      envelope.schema_version !== 1 || envelope.algorithm !== 'Ed25519' ||
      trust.key_id !== envelope.key_id) throw new Error('Untrusted campaign signer')
  const raw = Buffer.from(trust.public_key_base64, 'base64')
  const signature = Buffer.from(envelope.signature_base64, 'base64')
  const payload = Buffer.from(envelope.payload_base64, 'base64')
  if (raw.length !== 32 || signature.length !== 64 || payload.length > 65536 ||
      raw.toString('base64') !== trust.public_key_base64 ||
      signature.toString('base64') !== envelope.signature_base64 ||
      payload.toString('base64') !== envelope.payload_base64) throw new Error('Malformed campaign signature')
  const key = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: raw.toString('base64url') }, format: 'jwk' })
  if (!verify(null, Buffer.concat([Buffer.from('hermes-lite-acceptance-v1\n'), payload]), key, signature)) {
    throw new Error('Campaign signature rejected')
  }
  return true
}

if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  try {
    if (process.argv.length !== 4) throw new Error('Expected trust and signature paths')
    verifyCampaign(JSON.parse(readFileSync(process.argv[2], 'utf8')), JSON.parse(readFileSync(process.argv[3], 'utf8')))
    process.stdout.write('Verified campaign signature\n')
  } catch {
    process.stderr.write('Campaign signature verification failed\n')
    process.exitCode = 1
  }
}
