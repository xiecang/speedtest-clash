package source

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSpoolStorageFailureIsFatal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-spool")
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	spool := sourceSpool{file: file, names: map[string][]int{}}
	_, err = spool.decode([]byte("proxies: [{name: good, type: http, server: good, port: 443}]"))
	if !errors.Is(err, errSourceStorage) || !fatalSourceError(context.Background(), err) {
		t.Fatal("storage error was treated as a bad provider", err)
	}
	if err = spool.rollback(0, 0); !errors.Is(err, errSourceStorage) {
		t.Fatal("rollback error was hidden", err)
	}
}
