package wiki

import (
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// The postsByTopicMap and landing-stats tests lived here until the
// 2026-08-17 declutter removed both helpers with the sidebar tree and
// stats card they fed.
