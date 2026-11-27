export const isAgentEndpoint = kind => kind === 'openai-agent' || kind === 'anthropic-agent';
export const anthropicBase = endpoint => endpoint.replace(/\/v1$/, '');
export const noRedirect = (input, init) => fetch(input, { ...init, redirect: 'error' });
const FALLBACK_REASONING = ['low', 'medium', 'high', 'xhigh'];
const reasoningSupported = item => item?.capabilities?.effort?.supported
  ?? item?.capabilities?.reasoning?.supported ?? item?.supports_reasoning;

function modelReasoning(item) {
  const levels = item.supported_reasoning_efforts ?? item.reasoning_effort_levels;
  if (Array.isArray(levels))
    return [...new Set(levels.filter(level => typeof level === 'string' && /^[A-Za-z0-9_-]{1,32}$/.test(level)))];
  const effort = item.capabilities?.effort;
  if (reasoningSupported(item) === false) return [];
  const effortEntries = Object.entries(effort ?? {}).filter(([, metadata]) => typeof metadata?.supported === 'boolean');
  if (effortEntries.length) return effortEntries.filter(([, metadata]) => metadata.supported).map(([level]) => level);
  const advertised = Object.entries(item).flatMap(([key, supported]) => {
    const level = /^supports_([A-Za-z0-9_-]{1,32})_reasoning_effort$/.exec(key)?.[1];
    return level && supported === true ? [level] : [];
  });
  if (advertised.length) return advertised;
  return undefined;
}
function catalogError(detail, credential = false) {
  return Object.assign(new Error(detail), {catalog: true, credential});
}
function transportError(provider, signal, error) {
  const timeout = signal?.reason?.name === 'TimeoutError' || error.name === 'TimeoutError'
    || ['ETIMEDOUT','UND_ERR_CONNECT_TIMEOUT','UND_ERR_HEADERS_TIMEOUT','UND_ERR_BODY_TIMEOUT'].includes(error.cause?.code);
  return catalogError(timeout ? 'Request timed out' : 'Cannot reach ' + provider.name + '. Check your connection.');
}
export async function endpointModels(provider, apiKey, signal = AbortSignal.timeout(4000), { discoverGateway = false } = {}) {
  const anthropic = provider.kind === 'anthropic-agent';
  const url = anthropic ? anthropicBase(provider.endpoint) + '/v1/models' : provider.endpoint + '/models';
  const headers = anthropic ? { 'x-api-key': apiKey, 'anthropic-version': '2023-06-01' } : { authorization: 'Bearer ' + apiKey };
  let response;
  try { response = await noRedirect(url, { headers, signal }); }
  catch (error) { throw transportError(provider, signal, error); }
  if ([408,504].includes(response.status)) throw catalogError('Request timed out');
  if ([401, 403].includes(response.status)) throw catalogError('The endpoint rejected the API key.', true);
  const catalogUnavailable = [404, 405, 501].includes(response.status);
  if (!catalogUnavailable && !response.ok) throw catalogError(`The model catalog returned HTTP ${response.status}.`);
  let body = {};
  if (!catalogUnavailable) {
    try { body = await response.json(); }
    catch (error) {
      if (error instanceof SyntaxError) throw catalogError('The model catalog returned invalid JSON.');
      throw transportError(provider,signal,error);
    }
  }
  const rows = (Array.isArray(body?.data) ? body.data : []).filter(item => typeof item?.id === 'string' && item.id.trim());
  const models = rows.map(item => {
    const reasoning = modelReasoning(item);
    return { id: item.id, label: item.display_name || item.id, ...(reasoning ? { reasoning } : {}),
      ...(item.is_default === true ? { default: true } : {}) };
  });
  const withFallback = (model, supported) => model.reasoning === undefined && supported === true
    ? { ...model, reasoning: FALLBACK_REASONING } : model;
  if (!discoverGateway) return models.map((model, index) => withFallback(model, reasoningSupported(rows[index])));
  if (models.length && models.every(model => model.reasoning !== undefined)) return models;
  const base = provider.endpoint.replace(/\/v1$/, '');
  const metadata = suffix => noRedirect(base + suffix, {
    headers: { authorization: 'Bearer ' + apiKey }, signal,
  }).then(res => res.ok ? res.json() : null).then(info => Array.isArray(info?.data) ? info.data : [], () => []);
  const [groups, deployments] = await Promise.all([metadata('/model_group/info'), metadata('/model/info')]);
  const byGroup = new Map(groups.filter(item => typeof item?.model_group === 'string').map(item => [item.model_group, item]));
  // When a compatible gateway omits /models, use its public model group and deployment aliases.
  const gatewayIds = new Set([...byGroup.keys(), ...deployments.map(item => item?.model_name)]
    .filter(id => typeof id === 'string' && id.trim()));
  const catalog = models.length ? models : [...gatewayIds].map(id => ({ id, label: id }));
  return catalog.map((model, index) => {
    if (model.reasoning !== undefined) return model;
    const group = byGroup.get(model.id);
    const groupLevels = group && modelReasoning(group);
    if (group && groupLevels !== undefined) return { ...model, reasoning: groupLevels };
    const routes = deployments.filter(item => item?.model_name === model.id).map(item => item.model_info);
    const levels = routes.map(info => info ? modelReasoning(info) : undefined);
    if (levels.length && levels.every(value => value !== undefined))
      return { ...model, reasoning: levels[0].filter(level => levels.every(values => values.includes(level))) };
    const supportingRoutes = routes.length > 0 && routes.every(info => reasoningSupported(info) === true);
    return withFallback(model, reasoningSupported(group) ?? reasoningSupported(rows[index]) ?? supportingRoutes);
  });
}
