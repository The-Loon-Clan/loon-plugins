package ranks

import (
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Provision's whole job on the web leg is to put two capabilities on the
// extension registry. Nothing else in the tree checks that it did: both
// contracts are looked up with a not-found path that degrades quietly by
// design, so a missing Register is invisible until someone notices a badge
// that never renders. GroupDisplay shipped exactly that way — implemented,
// contract published in pluginapi, never registered.
//
// No database: Provision only needs a non-nil handle to build its PGStore, and
// sqlx.Open does not dial. That keeps this a plain unit test, which is the
// point — a registry regression should fail without Postgres in the loop.

func provisionLeg(t *testing.T, process string) *core.Core {
	t.Helper()
	db, err := sqlx.Open("postgres", "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	c, err := core.New(core.Deps{
		Process:       process,
		Storage:       core.NewStorage(db),
		Users:         core.NewUsers(core.UsersAdapter{}),
		Auth:          core.NewAuth(core.AuthAdapter{}),
		RBAC:          core.NewRBAC(),
		Scheduler:     core.NewScheduler(core.SchedulerAdapter{}),
		Router:        core.NewRouter(core.RouterAdapter{}),
		Logger:        core.DefaultLogger(),
		Config:        core.NewConfig(nil),
		Notifications: core.NewNotifications(core.NotificationsAdapter{}),
		Points:        core.NewPoints(core.PointsAdapter{}),
		Entitlements:  core.NewEntitlements(core.EntitlementsConfig{Store: core.NewMemEntitlementStore()}),
		HTTPClient:    core.NewHTTPClient(),
		Errors:        core.NewErrorReporter(core.ErrorAdapter{}),
	})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if err := (&Plugin{}).Provision(c); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	return c
}

func provisionWeb(t *testing.T) *core.Core { return provisionLeg(t, "web") }

// Resolved the way the store plugin resolves it (a bare Lookup + assertion —
// there is no typed helper for this one), so the test fails for the same
// reasons the consumer would.
func TestProvision_PublishesRankGranter(t *testing.T) {
	c := provisionWeb(t)

	v, ok := c.Lookup(pluginapi.RankGranterName)
	if !ok {
		t.Fatal("RankGranter is not on the registry — the store plugin refuses to provision without it")
	}
	if _, ok := v.(pluginapi.RankGranter); !ok {
		t.Errorf("registered value is %T, not a pluginapi.RankGranter", v)
	}
}

func TestProvision_PublishesGroupDisplay(t *testing.T) {
	c := provisionWeb(t)

	d, ok := pluginapi.LookupGroupDisplay(c)
	if !ok {
		t.Fatal("GroupDisplay is not on the registry — every badge consumer would degrade to no badge, silently")
	}
	if d == nil {
		t.Error("GroupDisplay resolved to nil")
	}
}

func TestProvision_PublishesGroupAudit(t *testing.T) {
	c := provisionWeb(t)

	a, ok := pluginapi.LookupGroupAudit(c)
	if !ok {
		t.Fatal("GroupAudit is not on the registry — the admin page's rank history would silently vanish")
	}
	if a == nil {
		t.Error("GroupAudit resolved to nil")
	}
}

// The worker leg must publish the capabilities too. The discord and irc bots
// run there and read badges to colour chat, so a web-only publish left them
// resolving a capability that did not exist in their process — which degrades
// silently to no badge, exactly like the unregistered-Provision bug above.
func TestProvision_WorkerLegPublishesTheCapabilities(t *testing.T) {
	c := provisionLeg(t, "worker")

	if _, ok := pluginapi.LookupGroupDisplay(c); !ok {
		t.Error("GroupDisplay absent on the worker leg — the bots colour nothing")
	}
	if _, ok := pluginapi.LookupGroupAudit(c); !ok {
		t.Error("GroupAudit absent on the worker leg")
	}
}

// The inverse of what this used to assert. The worker leg REQUIRED SetJobDeps
// before the expiry job moved onto core.Scheduler; now it requires nothing at
// all, and that is the property the lift depends on — a plugin that needs
// something handed to it before Boot cannot be dropped into another site.
//
// Provisioning every leg with a bare Core, and nothing else, is the closest
// this suite gets to "would this work in loon-plugins".
func TestProvision_NeedsNothingHandedIn(t *testing.T) {
	for _, process := range []string{"web", "worker", "all"} {
		t.Run(process, func(t *testing.T) {
			// provisionLeg fails the test if Provision errors, so reaching the
			// end IS the assertion. Nothing is set up beforehand.
			c := provisionLeg(t, process)
			if _, ok := pluginapi.LookupGroupDisplay(c); !ok {
				t.Errorf("%s leg did not publish GroupDisplay", process)
			}
		})
	}
}
