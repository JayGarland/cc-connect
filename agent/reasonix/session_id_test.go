package reasonix

import (
	"context"
	"testing"
)

func TestNewSession_EmptySessionID_GeneratesUUID(t *testing.T) {
	ts := mockReasonixServe(t)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "", "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	id := sess.CurrentSessionID()
	if id == "" {
		t.Fatal("CurrentSessionID() returned empty string — relay session ID will not be persisted")
	}
	if len(id) < 10 {
		t.Fatalf("session ID too short: %q", id)
	}
	t.Logf("generated session ID: %s", id)
}
