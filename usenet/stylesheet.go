package usenet

// usenetCSS is what this plugin's fragments used to carry inline.
//
// A fragment cannot reach the document head, so its CSS travelled inside the
// markup -- re-sent in every view that used it, and inline, which is what stops
// the host dropping style-src 'unsafe-inline'. Registered once at Provision
// now; the host gives it a URL, a content hash and a year of caching.
// See docs/BACKLOG.md #13 in loon-demo-site.
//
// Rules are unchanged and in template order. RegisterStylesheet no-ops on a
// host with no sink, where these pages draw unstyled -- visible rather than
// silent, which is the right failure for a missing seam.
const usenetCSS = `/* from settings.html */
/* Plugin-owned component CSS. These classes exist in no host theme (they were
   demo-theme rules), so the fragment ships them itself; colors come from
   Bootstrap's root tokens, which every loon host loads, rather than from any
   theme-specific variable. Covers the whole unified page, including the
   embedded Crawlers/Filters fragments. */
.cov-bar { display:flex; height:12px; border-radius:6px; overflow:hidden; background:rgba(128,128,128,.18); min-width:120px; }
.cov-bar span { display:block; height:100%; }
.cov-unknown { background:rgba(128,128,128,.18); width:100%; }
.cov-legend { display:flex; gap:1.25rem; font-size:.8rem; opacity:.75; margin:.3rem 0 .8rem; flex-wrap:wrap; }
.cov-legend .dot { display:inline-block; width:10px; height:10px; border-radius:2px; margin-right:.3rem; vertical-align:middle; box-shadow:none; }
.cov-line { display:flex; align-items:center; gap:.5rem; }
.cov-cells { display:flex; gap:1px; height:9px; min-width:120px; flex:1 1 auto; }
.cov-pct { font-size:.75rem; color:var(--bs-secondary-color); font-variant-numeric:tabular-nums; min-width:2.6rem; text-align:right; }
.cov-cells i { flex:1 1 0; border-radius:1px; background:rgba(128,128,128,.18); }
.cov-cells .cc1 { background:color-mix(in srgb, var(--bs-success) 30%, transparent); }
.cov-cells .cc2 { background:color-mix(in srgb, var(--bs-success) 60%, transparent); }
.cov-cells .cc3 { background:var(--bs-success); }
.empty { opacity:.7; padding:2rem; text-align:center; border:1px dashed rgba(128,128,128,.35); border-radius:8px; }
/* Provider pills: the host theme colors .nav-link blue, which lands blue-on-
   blue once Bootstrap's active pill paints its primary background. Pin the
   pill text explicitly in both states. */
.nav-pills .nav-link { color: var(--bs-secondary-color); }
.nav-pills .nav-link.active { color: #fff; }

`
