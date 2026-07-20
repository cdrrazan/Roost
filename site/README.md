# roost website

A single self-contained page (`index.html`, no external assets, no build
step) meant for Cloudflare Pages.

## Deploy to Cloudflare Pages

CLI:

```bash
npx wrangler pages deploy site --project-name roost
```

Or via the dashboard: **Workers & Pages → Create → Pages → Upload assets** and
upload this folder — or connect the GitHub repo with build output directory
`site` and no build command.

The page is theme-aware (light/dark via `prefers-color-scheme`) and has no
runtime dependencies, so there is nothing to keep updated except the content.
