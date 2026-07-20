# roost website

A single static page (`index.html`, no build step) meant for Cloudflare
Pages. The only external dependency is the Google Sans webfont from Google
Fonts; everything else is inline.

## Deploy to Cloudflare Pages

CLI:

```bash
npx wrangler pages deploy site --project-name roost
```

Or via the dashboard: **Workers & Pages → Create → Pages → Upload assets** and
upload this folder — or connect the GitHub repo with build output directory
`site` and no build command.

The page is theme-aware (light/dark via `prefers-color-scheme`); apart from
the webfont there is nothing to keep updated except the content.
