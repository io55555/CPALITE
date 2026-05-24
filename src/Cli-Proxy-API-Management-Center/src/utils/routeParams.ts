const ROUTE_INDEX_PATTERN = /^(0|[1-9]\d*)$/;

export function parseRouteIndexParam(value: string | undefined): number | null {
  // 避免 parseInt 接受 "1abc" 这类非规范路由参数。
  if (!value || !ROUTE_INDEX_PATTERN.test(value)) return null;

  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}
