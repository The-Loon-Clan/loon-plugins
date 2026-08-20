# applications plugin

A front door for a closed site. Somebody who cannot register writes an
application; staff read the queue; an approval becomes a real invite.

The gap it fills is narrower than it looks. A loon host already ships three ways
to join — open, invite-only, closed — and they are a setting, not a seam, so a
site wanting a fourth had nowhere to put it. This plugin adds one **and the way
to add one**: `pluginapi.RegistrationModePrefix` is a registry prefix, so the
access page offers whatever modes are registered beside the built-in three, and
the next plugin with an idea about joining needs no host change either.

**Deliberately absent:** interviews. The gap list calls this
"applications / interviews" and the second half is a conversation — a thread
between an applicant and staff — which is a different feature with a different
surface. What ships is a written application, a queue, and a decision.

## Surface

| Route / view | Access | Notes |
|---|---|---|
| `GET /p/apply` | public | The form. This is the one page on a closed site a stranger can reach. |
| `POST /p/apply/submit` | public | **The whole risk surface.** See below. |
| `GET /admin/p/applications` | mod | The queue. |
| `POST /admin/p/applications/decide` | mod | Approve → mint an invite and email it. Refuse → record it. |
| Widget `apply-cta` | public | The call to action on the host's sign-up page. A widget rather than a host template edit, because the host owns that page's layout and this plugin only has something to say when its mode is the active one. |

## The public POST

Every refusal is before anything is written:

- an address that already has an application waiting — one queue entry per
  person, not one per time they pressed the button;
- a shape check on the address, because an approval EMAILS it and an invite sent
  nowhere is a slot given to nobody;
- a bounded body.

What it deliberately does **not** do is tell the applicant whether their address
already has an account. That is the same enumeration oracle the invite form
refuses to be, and here it would be worse: this endpoint takes no credentials at
all.

The client IP is stored **hashed**. The operator needs to see that six
applications came from one place; nobody needs the address itself sitting in a
table a moderator can read, and a hash answers the question that is actually
asked. It is unsalted — the site's own salt is not reachable from a plugin — and
therefore reversible by anybody who guesses an address and compares, which is
why the column is never displayed, only compared.

## Data

Owns `applications.applications`: the address, an optional username, the body,
the IP hash, and the decision with who made it and when.

**Not here:** the invite. An approval calls the host's `InviteIssuer`, so the
code, its window, its email and its place in the invite chain are all the host's
— which is what keeps an approved applicant indistinguishable from anybody else
who was invited.

## Dependencies

| Needs | Why |
|---|---|
| `core.Storage.SchemaDB` | Its own schema. |
| `core.Auth` | The queue is staff-only. |
| `pluginapi.InviteIssuer` (`auth.invite.issue`) | An approval has to become a real invite, and minting one is not a plugin's business — same mint, same window, same email, same chain as any other invite. |
| `pluginapi.CSRFTokenFunc` | Every form, including the public one. |

## Hooks & callbacks

- **Publishes:** `auth.regmode.apply` (`pluginapi.RegistrationModeInfo`) — a way
  to join, offered on the host's access page.
- **Consumes:** `auth.invite.issue`, `csrf.token`.
- **Views:** `SlotSitePage` (`apply`), `SlotAdminPage` (`applications`).
- **Widget:** `apply-cta`.

**The trap in the mode contract**, learned by shipping it: `AllowsSignup`
governs the PAGE, not the endpoint. With it false, an approved applicant
arriving with a perfectly good invite was refused at registration — the invite
check has to come first. A mode that forbids open signup also needs
`ActionHref`/`ActionLabel`, or the register page is a dead end for anybody whose
only route in is the thing that mode offers.

## Lifecycle

`Provision` opens the schema, registers the mode, both views and the widget.
Nothing to resolve in `Start`: the invite issuer comes from the HOST, and
anything the host registers before `core.Boot` is available at Provision.

## Files

```
plugin.go     metadata, the mode registration, views, the widget
store.go      the queue
views.go      the form, submit, the queue page, decide, hashIP
migrations/
templates/    applications_apply.html, applications_queue.html,
              applications_cta.html
```

## Testing

**No Go tests.** Verified by hand against a running site: a submission
appearing in the queue; a second submission from the same address refused; a
malformed address refused; an approval minting an invite that actually
registers; and the two mode bugs above, both found that way.

**Known gaps.** `looksLikeEmail` and `hashIP` are pure and untested. Nothing
pins the enumeration rule — that a known address and an unknown one produce the
same response — which is exactly the kind of property that regresses silently
when somebody adds a helpful error message.
