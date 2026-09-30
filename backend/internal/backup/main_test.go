package backup

import (
	"os"
	"testing"
)

// defaultProductionKDF is the parameter set the panel ships with. It is
// captured before TestMain lowers defaultKDF for the rest of the suite.
var defaultProductionKDF = defaultKDF

func TestMain(m *testing.M) {
	// Argon2id with the production parameters takes a noticeable fraction
	// of a second per derivation. The key file written by keyring.set uses
	// defaultKDF, so the suite lowers it to the cheapest accepted set; the
	// production values are tested in TestProductionKDFRoundTrip and
	// TestKeyFileUsesProductionParameters.
	defaultKDF = fastKDF
	os.Exit(m.Run())
}
