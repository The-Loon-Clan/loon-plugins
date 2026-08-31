package downloads

// downloadsCSS styles the admin report page. Registered once at Provision; the
// host gives it a URL, a content hash and a year of caching.
// RegisterStylesheet no-ops on a host with no sink, where the page draws
// unstyled -- visible rather than silent, which is the right failure for a
// missing seam.
//
// The stat tiles themselves are the host's .stat-tile component; only the
// strip that rows them up is this plugin's.
const downloadsCSS = `/* from downloads_admin.html */
        .stat-strip { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 1rem; }
        .stat-strip .stat-tile { flex: 1 1 120px; }
/* from downloads_member.html: the setup checklist's <ol class="stack">.
   MIRRORS the demo host's own global utility (loon-demo-site
   web/static/css/layout.css .stack) byte for byte -- a divergent rule here
   (a margin-top chain, a different gap) would combine with the demo's flex
   gap and change spacing there. Identical rules compute identically. */
        .stack { display: flex; flex-direction: column; gap: 1rem; }
`
