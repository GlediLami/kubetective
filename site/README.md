# KubeTective docs site

Static GitHub Pages site (single page, hand-written HTML - no framework, no
build). Served at https://gledilami.github.io/kubetective/.

## Edit

Edit `index.html` / `style.css`, keep it in sync with the README when
features change (the README is the landing page; this site is its
one-page mirror for quick reference).

## Deploy

CI workflow `.github/workflows/pages.yml` uploads `site/` and deploys to
GitHub Pages on every push to `main`. No local tooling needed; verify with:

```sh
python3 -m http.server -d site 8080   # then open http://localhost:8080
```