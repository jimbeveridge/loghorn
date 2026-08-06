package entry

import "testing"

func TestParseSeverityOrdering(t *testing.T) {
	if ParseSeverity("error") != SevError {
		t.Fatalf("error should parse to SevError, got %v", ParseSeverity("error"))
	}
	if ParseSeverity("CRITICAL") != SevCritical {
		t.Fatalf("CRITICAL should parse to SevCritical")
	}
	if ParseSeverity("nonsense") != SevDefault {
		t.Fatalf("unknown severity should be SevDefault")
	}
	if !(SevWarning < SevError && SevError < SevCritical && SevCritical < SevEmergency) {
		t.Fatalf("severity constants are not ordered low->high")
	}
}

func TestSeverityString(t *testing.T) {
	if SevError.String() != "ERROR" {
		t.Fatalf("SevError.String() = %q, want ERROR", SevError.String())
	}
}
