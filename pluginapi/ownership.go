package pluginapi

// Ownership comparisons, in one place because getting them wrong is quiet.
//
// THE RULE: user id 0 is never an owner. It is two things at once —
//
//   - the id a request carries when NOBODY IS SIGNED IN. Most plugin routes
//     mount behind core.AuthService.Authenticate(), which in the site's public
//     access mode lets anonymous requests through by design; the viewer id is
//     then 0 and it reaches whatever the handler does next.
//   - the id reserved for THE SYSTEM. achievements refuses to grant a badge to
//     user 0 on exactly that ground.
//
// So `record.UserID == viewerID` is true for an anonymous viewer whenever the
// record's owner is 0 — and nothing in any schema here prevents a 0 from being
// stored. On 20 Aug 2026 no ownership column in the live database held one
// (63 columns checked, one row in login_logs, which is a failed sign-in and
// correct). The whole class of comparison rests on that, and nothing enforced
// it, which is not a thing to leave resting.
//
// This bug was found four times in two days, in four different plugins, always
// by accident:
//
//	playlists.owned()        compared a stored owner to a viewer id of 0
//	playlists.Show()         the same, two lines above a correctly-guarded one
//	comments.Delete()        used 0 to MEAN "staff", so 0 also meant "everyone"
//	tickets.ticketVisibleTo  a support ticket, visible on an id match alone
//
// Each was correct in the code around it and wrong on the one input nobody
// passes by hand. Hence a named function: the rule gets stated once, and a
// reviewer looking for it can grep for it.

// ID is any of the integer types this repo stores a user id in. Plugins are
// inconsistent about int vs int64 — tickets uses int, most use int64 — and a
// helper that forced a conversion at every call site would be one more place
// to make a mistake.
type ID interface {
	~int | ~int32 | ~int64
}

// OwnedBy reports whether viewer owns a record belonging to owner.
//
// False for a viewer id of 0 or below, ALWAYS, whatever the owner is. Use this
// anywhere an ownership match grants something: showing a private record,
// offering an edit control, accepting a write.
func OwnedBy[T ID](ownerID, viewerID T) bool {
	return viewerID > 0 && ownerID == viewerID
}

// VisibleTo reports whether a viewer may see a record: the owner, or somebody
// the caller has already decided is privileged.
//
// The second argument is a decision, not an id — a plugin resolves "is this
// person staff" its own way, and passing the answer keeps that out of here.
// It is separate from the ownership match on purpose: encoding "staff" as a
// magic id is what made comments.Delete remove anybody's comment.
func VisibleTo[T ID](ownerID, viewerID T, privileged bool) bool {
	return privileged || OwnedBy(ownerID, viewerID)
}
