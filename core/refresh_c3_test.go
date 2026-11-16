package core

import (
	"context"
	"testing"
	"time"

	"brod/store"
)

// источник с недопустимым адресом, уже лежащий в базе (старая запись или запись мимо AddSource), при обновлении пропускается.
func TestRefreshSkipsStoredSourceWithBadURL(t *testing.T) {
	c := newCore(t, t.TempDir())
	t.Cleanup(func() { c.Close() })
	if err := c.st.UpsertSource(store.SourceRec{ID: "bad src", URL: "http://203.0.113.5/list.txt", Priority: 5, Enabled: true, UserAdded: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Refresh(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Sources {
		if s.Name == "bad src" {
			t.Fatalf("источник с http-адресом не должен был обновляться: %+v", s)
		}
	}
}
