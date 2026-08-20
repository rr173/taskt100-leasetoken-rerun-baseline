package lease

import "testing"

func TestAcquireAdviceReportsActiveLeaseConflict(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Acquire("advice-resource", "holder-a", 30); err != nil {
		t.Fatal(err)
	}
	advice, err := m.AcquireAdvice("advice-resource")
	if err != nil {
		t.Fatal(err)
	}
	if advice.Allowed {
		t.Fatalf("advice must reject an actively held resource: %+v", advice)
	}
	if advice.Reason != ErrConflict.Error() {
		t.Fatalf("reason = %q, want %q", advice.Reason, ErrConflict.Error())
	}
}
