package messages

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// viewer is the signed-in user as this package needs them: an int id, a name,
// and a role test.
//
// It exists to absorb one impedance mismatch. core.User carries an int64 id,
// while these tables — and therefore every store method lifted with them —
// key on int. Converting at the 23 call sites would have meant editing the
// lifted handlers line by line, which is exactly what a verbatim port is
// trying to avoid; converting once here leaves the handler bodies identical
// to the origin site's, so a future diff between them is signal rather than
// noise.
type viewer struct {
	ID       int
	Username string
	role     core.Role
}

// AtLeast mirrors core.User.AtLeast so the lifted `user.AtLeast(core.RoleMod)`
// calls read unchanged.
func (v *viewer) AtLeast(r core.Role) bool { return v != nil && v.role >= r }

// currentUser resolves the session user, or nil when anonymous. Handlers
// redirect to /login on nil, exactly as they did on the origin site.
func (h *Handlers) currentUser(c *gin.Context) *viewer {
	if h.auth == nil {
		return nil
	}
	u, ok := h.auth.CurrentUser(c)
	if !ok || u == nil {
		return nil
	}
	return &viewer{ID: int(u.ID), Username: u.Username, role: u.Role}
}

// usersByID / usersByName / allUsers are the user lookups the composer and the
// DM send path need, over core.Users rather than a host repository.
func (h *Handlers) usersByID(ctx context.Context, id int) (*viewer, error) {
	if h.users == nil {
		return nil, nil
	}
	u, err := h.users.GetByID(ctx, int64(id))
	if err != nil || u == nil {
		return nil, err
	}
	return &viewer{ID: int(u.ID), Username: u.Username, role: u.Role}, nil
}

func (h *Handlers) usersByName(ctx context.Context, name string) (*viewer, error) {
	if h.users == nil {
		return nil, nil
	}
	u, err := h.users.GetByUsername(ctx, name)
	if err != nil || u == nil {
		return nil, err
	}
	return &viewer{ID: int(u.ID), Username: u.Username, role: u.Role}, nil
}

// allUsers backs the composer's recipient picker, or returns nothing when the
// host did not supply the optional seam.
func (h *Handlers) allUsers(ctx context.Context) ([]UserOption, error) {
	if deps == nil || deps.ListUsers == nil {
		return nil, nil
	}
	return deps.ListUsers(ctx)
}
