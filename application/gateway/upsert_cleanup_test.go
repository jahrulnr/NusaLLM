package appgateway

import (
	"context"
	"errors"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
)

type deleteFailingSecrets struct {
	drepo.SecretStore
}

func (deleteFailingSecrets) Delete(context.Context, string) error {
	return errors.New("vault cleanup failed")
}

// Upsert writes the record before deleting the old canonical vault entry.
// If that cleanup fails after the record committed, reporting a write
// failure leaves the caller retrying a PUT whose old key is already gone:
// the retry sees the empty-key record and cannot recreate what it lost.
// The record may not be reported as failed once it has been replaced.
func TestUpsertClearKeyDoesNotReportFailureAfterCommit(t *testing.T) {
	base, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	secrets := deleteFailingSecrets{SecretStore: base}
	repo := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	app := New(repo, secrets, nil)

	key := "sk-live"
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion,
		Endpoint: "https://example.test", ApiKey: &key,
		Models: []ModelInput{{ID: "m"}},
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	clear := ""
	in.ApiKey = &clear
	_, err = app.Upsert(t.Context(), in)
	if err == nil {
		return // tolerating the orphaned vault entry is the other safe contract
	}
	info, getErr := app.Get(t.Context(), "g")
	if getErr != nil || !info.KeySet {
		t.Fatalf("Upsert reported failure after committing the key clear: err=%v keySet=%v", err, info.KeySet)
	}
}
