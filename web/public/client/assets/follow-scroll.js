export const FOLLOW_DISTANCE_PX = 72;

export function isNearConversationEnd(metrics, threshold = FOLLOW_DISTANCE_PX) {
  if (!metrics) return true;
  const scrollHeight = Number(metrics.scrollHeight) || 0;
  const scrollTop = Number(metrics.scrollTop) || 0;
  const clientHeight = Number(metrics.clientHeight) || 0;
  return scrollHeight - scrollTop - clientHeight <= threshold;
}
