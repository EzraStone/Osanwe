// Diagnostic verdicts are deliberately stricter than "no packets were seen".
// Only fixed error names are printable; never emit exception messages or stacks.
export function safeExceptionName(error) {
  const allowed = [
    'Error', 'TypeError', 'RangeError', 'ReferenceError', 'SyntaxError',
    'AbortError', 'InvalidAccessError', 'InvalidStateError', 'NotAllowedError',
    'NotFoundError', 'NotSupportedError', 'OperationError', 'SecurityError', 'TimeoutError',
  ];
  try {
    const name = error?.name;
    return allowed.includes(name) ? name : 'UnknownError';
  }
  catch { return 'UnknownError'; }
}

export function diagnosticVerdict(result, calibrated = false) {
  const outcome = result.crashed ? 'crashed'
    : result.interrupted ? 'incomplete'
    : result.stage === 'rejected' ? 'rejected'
    : result.stage === 'completed' ? 'completed' : 'incomplete';
  const verdict = (passed, reason) => ({ outcome, passed, reason });
  if (!['rtc', 'http'].includes(result.protocol)) return verdict(false, 'unknown-protocol');
  const count = result.protocol === 'rtc' ? result.packets : result.requests;
  if (!Number.isSafeInteger(count) || count < 0) return verdict(false, 'invalid-listener-count');
  if (result.scenario === 'control') {
    if (outcome !== 'completed') return verdict(false, 'control-did-not-complete');
    return count > 0 ? verdict(true, 'calibrated') : verdict(false, 'control-did-not-reach-listener');
  }
  if (!calibrated) return verdict(false, 'uncalibrated-control');
  if (count !== 0) return verdict(false, 'protected-traffic-observed');
  if (outcome === 'crashed' || outcome === 'incomplete') return verdict(false, outcome);
  if (result.protocol === 'rtc') {
    // An arbitrary constructor/offer rejection is not evidence of confinement.
    return outcome === 'completed' ? verdict(true, 'completed-without-traffic')
      : verdict(false, 'unexpected-rtc-rejection');
  }
  // fetch TypeError alone also covers unrelated failures. Require a matching
  // browser policy event and the successfully calibrated loopback listener.
  return outcome === 'rejected' && result.exceptionName === 'TypeError' && result.connectBlocked === true
    ? verdict(true, 'verified-connect-src-denial') : verdict(false, 'unverified-http-denial');
}
