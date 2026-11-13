// DSH's authoring schema is a strict subset of JSON Schema. Preserve every
// protocol field and server validation; express unsupported numeric/pattern
// constraints as descriptions rather than pretending the host enforces them.
export function nativeValueSchema(value) {
  const out = {};
  if (value.description) out.description = value.description;
  const constraints = ['minimum','maximum','pattern','minLength'].filter(k => value[k] !== undefined).map(k => `${k}=${value[k]}`);
  if (constraints.length) out.description = [out.description, 'Server validates: '+constraints.join(', ')].filter(Boolean).join(' ');
  if (value.anyOf || value.oneOf) out.oneOf = (value.anyOf || value.oneOf).map(nativeValueSchema);
  else {
    out.type = value.type || 'json';
    if (value.enum) out.enum = [...value.enum];
    if (value.const !== undefined) out.const = value.const;
    if (value.type === 'object') {
      out.additionalProperties = value.additionalProperties ?? true;
      if (value.properties) out.properties = nativeParameters(value);
    }
    if (value.type === 'array' && value.items) out.items = nativeValueSchema(value.items);
  }
  return out;
}
export function nativeParameters(schema) {
  return Object.fromEntries(Object.entries(schema.properties).map(([key,value]) => [key,{...nativeValueSchema(value),...(schema.required?.includes(key)?{required:true}:{})}]));
}
