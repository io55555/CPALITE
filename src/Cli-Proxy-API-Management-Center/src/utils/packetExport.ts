import type { PacketSet } from '@/services/api/packetCapture';

export type PacketKey = keyof PacketSet;

export const FOUR_PACKET_ITEMS: Array<{ key: PacketKey; title: string }> = [
  { key: 'client_request', title: '客户端发给CPA的完整数据包' },
  { key: 'upstream_request', title: 'CPA发给供应商的完整数据包' },
  { key: 'upstream_response', title: '供应商返回CPA的完整数据包' },
  { key: 'client_response', title: 'CPA发送给客户端的完整数据包' },
];

const sanitizeFilenamePart = (value: string, fallback: string): string => {
  const trimmed = value.trim();
  const safe = trimmed.replace(/[\\/:*?"<>|]+/g, '_').replace(/\s+/g, '_');
  return safe || fallback;
};

const formatTimestamp = (value?: string | number | Date | null): string => {
  const date = value ? new Date(value) : new Date();
  const normalized = Number.isFinite(date.getTime()) ? date : new Date();
  const pad = (part: number) => String(part).padStart(2, '0');
  return [
    normalized.getFullYear(),
    pad(normalized.getMonth() + 1),
    pad(normalized.getDate()),
    '_',
    pad(normalized.getHours()),
    pad(normalized.getMinutes()),
    pad(normalized.getSeconds()),
  ].join('');
};

export const buildFourPacketText = (packets: Partial<Record<PacketKey, string>>): string =>
  FOUR_PACKET_ITEMS.map(({ key, title }) => {
    const content = packets[key]?.trim() || '<empty>';
    return `-------------------------------------------------------------------------------------------------------- ${title}\n${content}`;
  }).join('\n\n');

export const buildFourPacketFilename = ({
  statusCode,
  timestamp,
  model,
}: {
  statusCode?: number | null;
  timestamp?: string | number | Date | null;
  model?: string | null;
}): string => {
  const errorPart = statusCode && statusCode > 0 ? `错误${statusCode}` : '错误未知';
  return `${errorPart}__${formatTimestamp(timestamp)}__4个数据包.${sanitizeFilenamePart(model || '', 'unknown-model')}.txt`;
};
