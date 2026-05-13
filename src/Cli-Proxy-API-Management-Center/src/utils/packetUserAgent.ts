const SECTION_MARKERS = {
  clientRequest: [
    '客户端发给CPA的完整数据包',
    '客户端发给 CPA 的完整数据包',
    'CPA收到客户端请求',
  ],
  upstreamRequest: [
    'CPA发给供应商的完整数据包',
    'CPA 发给供应商的完整数据包',
    'CPA发送给供应商',
  ],
};

const readNamedSection = (content: string, names: readonly string[]) => {
  for (const name of names) {
    const marker = `=== ${name} ===`;
    const start = content.indexOf(marker);
    if (start < 0) continue;
    const from = start + marker.length;
    const next = content.indexOf('=== ', from);
    return content.slice(from, next >= 0 ? next : undefined).trim();
  }
  return '';
};

export const extractHeaderValue = (packet: string | null | undefined, headerName: string) => {
  const text = (packet || '').trim();
  if (!text) return '';
  const escapedName = headerName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = text.match(new RegExp(`^${escapedName}\\s*:\\s*(.+)$`, 'im'));
  return match?.[1]?.trim() || '';
};

export const resolveUsageUserAgents = (detail: {
  client_ua?: string;
  upstream_ua?: string;
  raw_request?: string;
}) => {
  const rawRequest = detail.raw_request || '';
  const clientPacket = readNamedSection(rawRequest, SECTION_MARKERS.clientRequest) || rawRequest;
  const upstreamPacket = readNamedSection(rawRequest, SECTION_MARKERS.upstreamRequest);

  return {
    clientUA:
      detail.client_ua?.trim() ||
      extractHeaderValue(clientPacket, 'User-Agent') ||
      '-',
    upstreamUA:
      detail.upstream_ua?.trim() ||
      extractHeaderValue(upstreamPacket, 'User-Agent') ||
      '-',
  };
};
