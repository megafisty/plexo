package model

import "testing"

// TestCommandCatalogIsComplete ensures every declared Op is in the catalog and
// vice versa, so the primitive list cannot drift from the constants.
func TestCommandCatalogIsComplete(t *testing.T) {
	declared := []Op{
		OpSetCredentials, OpClearCredentials, OpPurgeCredentials, OpListCharacters,
		OpLogin, OpLogout, OpReconnect,
		OpSendMessage, OpSendLRP, OpSendTyping, OpJoin, OpLeave, OpSetStatus,
		OpSetIgnore, OpSetTracked,
		OpSetInterest,
	}
	if got, want := len(Commands()), len(declared); got != want {
		t.Fatalf("catalog has %d entries, want %d", got, want)
	}
	for _, op := range declared {
		spec, ok := LookupCommand(op)
		if !ok {
			t.Errorf("op %q missing from catalog", op)
			continue
		}
		if spec.Layer == "" || spec.Scope == "" {
			t.Errorf("op %q has empty layer/scope", op)
		}
		if spec.Summary == "" {
			t.Errorf("op %q has no summary", op)
		}
	}
	if _, ok := LookupCommand("not_a_real_op"); ok {
		t.Error("LookupCommand accepted an unknown op")
	}
}

// TestCommandsReturnsCopy ensures callers cannot mutate the catalog.
func TestCommandsReturnsCopy(t *testing.T) {
	got := Commands()
	got[0].Op = "tampered"
	if again := Commands(); again[0].Op == "tampered" {
		t.Fatal("Commands() exposed the backing array")
	}
}
