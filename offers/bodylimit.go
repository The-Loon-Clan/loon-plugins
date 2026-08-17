package offers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// maxBody caps the request body for a whole route group at n bytes, so every
// endpoint under it has a bound without each handler repeating one — including
// any added later.
//
// It composes safely with both neighbours of the middleware stack: it nests
// under the host's global body ceiling, and above a handler that sets its own
// tighter cap (UserCreateRequest does), because http.MaxBytesReader takes the
// smaller of two nested limits. Bodyless requests (the group's GET routes) read
// nothing, so the wrap costs them nothing.
//
// The plugin defines this rather than importing the host's equivalent: plugins
// do not import host packages (the boundary the import lint enforces), and a
// three-line wrapper is not worth a new seam in the Deps contract.
func maxBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		}
		c.Next()
	}
}
