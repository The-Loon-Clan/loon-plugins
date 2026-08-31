package achievements

// achievementsCSS is the one rule this plugin's templates need beyond the
// host's shared components. Registered once at Provision; the host gives it
// a URL, a content hash and a year of caching. RegisterStylesheet no-ops on
// a host with no sink, where the create form falls back to its children's
// own margins -- tighter, not broken, which is the right failure for a
// missing seam.
//
// .stack (the create-achievement form) MIRRORS the demo host's own global
// utility (loon-demo-site web/static/css/layout.css .stack) byte for byte:
// that host already defines it, so a divergent rule here (a margin-top
// chain, a different gap) would combine with the demo's flex gap and change
// spacing there. Identical rules compute identically on both hosts.
const achievementsCSS = `/* from achievements_admin.html */
        .stack { display: flex; flex-direction: column; gap: 1rem; }
`
