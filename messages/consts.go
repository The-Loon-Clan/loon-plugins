package messages

// entDMInitiate is the entitlement that answers "may this user START a
// conversation?" — replying to an existing thread is not gated.
//
// A string rather than an import from whichever plugin grants it: the ranks
// plugin publishes entitlements, the messaging plugin consumes an answer, and
// neither should have to compile against the other for that to work.
const entDMInitiate = "dm.initiate"

// notifDM is the notification kind for "you have a new DM". It doubles as the
// user's per-kind preference key, so changing it silently opts everyone back
// in to a channel they had muted.
const notifDM = "dm"
