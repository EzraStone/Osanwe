// A result may update the UI only while its originating check still owns it.
export class ConnectionCheck {
  #current = null;
  start(timeoutMs = 25000) {
    this.cancel();
    const check = { controller: new AbortController(), timedOut: false };
    check.timer = setTimeout(() => { check.timedOut = true; check.controller.abort(); }, timeoutMs);
    this.#current = check;
    return check;
  }
  owns(check) { return this.#current === check; }
  finish(check) {
    clearTimeout(check.timer);
    if (this.owns(check)) this.#current = null;
  }
  cancel() {
    if (!this.#current) return false;
    const check = this.#current;
    this.#current = null;
    clearTimeout(check.timer);
    check.controller.abort();
    return true;
  }
}
