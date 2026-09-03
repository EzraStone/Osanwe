export class RequestCapacity {
  constructor({ maxConcurrent = 3, maxRequests = 30, windowMs = 60_000, maxClients = 2048 } = {}) {
    this.maxConcurrent = maxConcurrent;
    this.maxRequests = maxRequests;
    this.windowMs = windowMs;
    this.maxClients = maxClients;
    this.clients = new Map();
  }

  acquire(client, now = Date.now()) {
    this.prune(now);
    let entry = this.clients.get(client);
    if (!entry) {
      if (this.clients.size >= this.maxClients) this.#evictOldestInactive();
      if (this.clients.size >= this.maxClients) return null;
      entry = { active: 0, recent: [], lastSeen: now };
      this.clients.set(client, entry);
    }

    entry.recent = entry.recent.filter((value) => value > now - this.windowMs);
    entry.lastSeen = now;
    if (entry.active >= this.maxConcurrent || entry.recent.length >= this.maxRequests) return null;
    entry.active += 1;
    entry.recent.push(now);

    let released = false;
    return () => {
      if (released) return;
      released = true;
      entry.active = Math.max(0, entry.active - 1);
    };
  }

  prune(now = Date.now()) {
    const cutoff = now - this.windowMs;
    for (const [client, entry] of this.clients) {
      entry.recent = entry.recent.filter((value) => value > cutoff);
      if (entry.active === 0 && entry.recent.length === 0 && entry.lastSeen <= cutoff) {
        this.clients.delete(client);
      }
    }
  }

  #evictOldestInactive() {
    let candidate = null;
    for (const [client, entry] of this.clients) {
      if (entry.active !== 0) continue;
      if (!candidate || entry.lastSeen < candidate.entry.lastSeen) candidate = { client, entry };
    }
    if (candidate) this.clients.delete(candidate.client);
  }
}
