package pgtest

import "testing"

// validSchemaName is the only thing in this package that runs without a
// database, and it is the one thing here that must not be wrong: schema and
// table names are concatenated into SQL because Postgres will not take an
// identifier as a bind parameter, so this function is the parameterisation.
func TestValidSchemaName(t *testing.T) {
	for _, good := range []string{
		"cosmetics_test",
		"a",
		"comments_store_test",
		"schema1",
		"x_1_y_2",
	} {
		if err := validSchemaName(good); err != nil {
			t.Errorf("validSchemaName(%q) = %v, want nil", good, err)
		}
	}

	for _, tc := range []struct{ in, why string }{
		{"", "empty"},
		{"Cosmetics", "uppercase; Postgres would fold it and the DROP would miss"},
		{"1schema", "starts with a digit"},
		{"_schema", "starts with an underscore"},
		{"has space", "a space"},
		{"has-dash", "a dash"},
		{`has"quote`, "a quote — the character that would end the identifier"},
		{"has;semicolon", "a statement separator"},
		{"drop cascade; DROP SCHEMA public CASCADE; --", "the whole point"},
		{"has'apostrophe", "an apostrophe"},
		{"has.dot", "a dot would make it schema-qualified"},
		{"héllo", "non-ASCII"},
		{"has\nnewline", "a newline"},
	} {
		if err := validSchemaName(tc.in); err == nil {
			t.Errorf("validSchemaName(%q) accepted it — %s", tc.in, tc.why)
		}
	}

	// Postgres truncates identifiers at 63 bytes, so a longer one would be
	// silently shortened — and the DROP that cleans up would then be aimed at
	// a name the CREATE did not make.
	long := ""
	for i := 0; i < 63; i++ {
		long += "a"
	}
	if err := validSchemaName(long); err != nil {
		t.Errorf("63 characters was refused: %v", err)
	}
	if err := validSchemaName(long + "a"); err == nil {
		t.Error("64 characters was accepted; Postgres would truncate it")
	}
}

// TestEnvDSNIsTheHostsName. The host repo's `make itest` exports this exact
// name, and the whole point of standardising was that one throwaway database
// serves both repos. If somebody renames it here, that stops being true and
// nothing fails — the tests simply skip.
func TestEnvDSNIsTheHostsName(t *testing.T) {
	if EnvDSN != "LOON_TEST_DSN" {
		t.Errorf("EnvDSN = %q; the host's Makefile and scripts/go.sh both say LOON_TEST_DSN", EnvDSN)
	}
}
