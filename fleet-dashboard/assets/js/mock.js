/* ============================================================================
   Fleet Dashboard — Mock data
   ----------------------------------------------------------------------------
   The demo runs entirely from this object. Replace it with your own data
   source (fetch a JSON endpoint on load, or hydrate server-side) to go live —
   see docs/INTEGRATION.md. The shapes here ARE the contract fleet.js renders.
   ============================================================================ */
window.FLEET = {
  brand: { name: "Fleet", tagline: "Control panel", user: "Alex Rivera", handle: "@alex" },

  // category: "main" | "utility" | "worker"
  apps: [
    { name: "analytics",  category: "main",    framework: "next",    db: "postgres", redis: false, runtime: "node-20",  state: "running", url: "https://analytics.example.com", repo: "https://github.com/acme/analytics", http: "200", cpu: 3.4,  mem: 412, cap: 768,  up: "Up 6 days",  restarts: 0, health: "healthy", featured: true, env: ["DATABASE_URL","NEXT_PUBLIC_API","SESSION_SECRET"] },
    { name: "billing",    category: "main",    framework: "rails",   db: "mysql",    redis: true,  runtime: "ruby-3.3", state: "running", url: "https://billing.example.com",   repo: "https://github.com/acme/billing",   http: "200", cpu: 1.9,  mem: 305, cap: 512,  up: "Up 6 days",  restarts: 0, health: "healthy", featured: true, env: ["DATABASE_URL","REDIS_URL","STRIPE_KEY"] },
    { name: "gateway",    category: "main",    framework: "node",    db: "",         redis: false, runtime: "node-20",  state: "running", url: "https://gateway.example.com",   repo: "https://github.com/acme/gateway",   http: "200", cpu: 6.1,  mem: 188, cap: 512,  up: "Up 6 days",  restarts: 1, health: "healthy", env: ["UPSTREAMS","RATE_LIMIT"] },
    { name: "cms",        category: "main",    framework: "django",  db: "postgres", redis: false, runtime: "py-3.12",  state: "running", url: "https://cms.example.com",       repo: "https://github.com/acme/cms",       http: "200", cpu: 2.2,  mem: 260, cap: 512,  up: "Up 2 days",  restarts: 0, health: "healthy", env: ["DATABASE_URL","SECRET_KEY"] },
    { name: "crm",        category: "main",    framework: "rails",   db: "mysql",    redis: true,  runtime: "ruby-3.3", state: "running", url: "https://crm.example.com",       repo: "https://github.com/acme/crm",       http: "502", cpu: 0.4,  mem: 495, cap: 512,  up: "Up 11 min",  restarts: 4, health: "unhealthy", env: ["DATABASE_URL","REDIS_URL"] },
    { name: "search",     category: "utility", framework: "rails",   db: "mysql",    redis: true,  runtime: "ruby-3.3", state: "running", url: "https://search.example.com",    repo: "https://github.com/acme/search",    http: "200", cpu: 8.7,  mem: 356, cap: 512,  up: "Up 3 hours", restarts: 0, health: "healthy", env: ["DATABASE_URL","REDIS_URL","INDEX_HOST"] },
    { name: "media",      category: "utility", framework: "next",    db: "",         redis: false, runtime: "node-20",  state: "running", url: "https://media.example.com",     repo: "https://github.com/acme/media",     http: "200", cpu: 1.1,  mem: 210, cap: 512,  up: "Up 6 days",  restarts: 0, health: "healthy", env: ["S3_BUCKET","CDN_URL"] },
    { name: "docs",       category: "utility", framework: "static",  db: "",         redis: false, runtime: "",         state: "running", url: "https://docs.example.com",      repo: "https://github.com/acme/docs",      http: "200", cpu: 0.1,  mem: 24,  cap: 128,  up: "Up 6 days",  restarts: 0, health: "", env: [] },
    { name: "legacy-api", category: "utility", framework: "node",    db: "postgres", redis: false, runtime: "node-18",  state: "exited",  url: "https://legacy.example.com",    repo: "https://github.com/acme/legacy",    http: "",    cpu: 0,    mem: 0,   cap: 512,  up: "",           restarts: 9, health: "", env: ["DATABASE_URL"] },
    { name: "billing-worker", category: "worker", framework: "rails", db: "mysql",   redis: true,  runtime: "ruby-3.3", state: "running", url: "", repo: "https://github.com/acme/billing", http: "", cpu: 0.8, mem: 141, cap: 384, up: "Up 6 days", restarts: 0, health: "", worker: true, env: ["DATABASE_URL","REDIS_URL"] }
  ],

  server: { label: "Cloud VPS · 4 vCPU / 8 GB · fra1", ip: "203.0.113.10", ssh: "ubuntu@203.0.113.10",
            host: "fleet-prod-1", os: "Ubuntu 24.04.1 LTS", cores: 4, ram: "8 GiB",
            diskUsed: "28 GiB", diskCap: "80 GiB", diskPct: 35, uptime: "12 days" },

  system: { images: 18, imagesSize: "6.2 GB", containers: 14, volumes: 7, volumesSize: "3.1 GB", reclaimable: "1.2 GB (14%)" },

  edge: { tunnel: "edge-tunnel", tunnelId: "b3f9a1c8d2e4", account: "9a1f0c7e", protected: true,
          hosts: ["*.example.com", "*.app.example.com", "*.cdn.example.com"] },

  // newest first; open incidents have resolved:null
  incidents: [
    { app: "legacy-api", label: "Legacy Api", kind: "down",     detail: "exited", since: "Jul 26 09:02", ago: "down 8m", open: true },
    { app: "crm",        label: "Crm",        kind: "degraded", detail: "up but returning HTTP 502", since: "Jul 26 08:47", ago: "down 23m", open: true },
    { app: "search",     label: "Search",     kind: "down",     detail: "exited", since: "Jul 26 06:10", ago: "resolved after 3m", open: false }
  ]
};
