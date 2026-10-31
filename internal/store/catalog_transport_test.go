package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestHostedCatalogueTransportSizes(t *testing.T) {
	dsn := os.Getenv("RADAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("hosted transport check")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, size := range []int{1024, 64 << 10, 1 << 20} {
		t.Run(time.Duration(size).String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var got int
			started := time.Now()
			if err := s.db.QueryRowContext(ctx, "SELECT OCTET_LENGTH(?::bytea)", make([]byte, size)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != size {
				t.Fatal("transport changed payload")
			}
			t.Logf("%d bytes in %s", size, time.Since(started))
		})
	}
}
