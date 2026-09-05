package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestExtensionUploadStateAndAuditAreAtomic(t *testing.T) {
	t.Run("stored", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		result := extensionUploadAtomicFixture("extension-a")
		installExtensionAuditFailure(t, database)
		if _, _, err := database.ReserveExtensionPackage(
			ctx, result, "actor-a", "request-a", "/state/extension-a",
		); err == nil {
			t.Fatal("extension reservation survived audit failure")
		}
		assertExtensionState(t, database, result.Manifest.ID, result.Manifest.Version, "", false)
		dropExtensionAuditFailure(t, database)
		if _, fresh, err := database.ReserveExtensionPackage(
			ctx, result, "actor-a", "request-a", "/state/extension-a",
		); err != nil || !fresh {
			t.Fatalf("reserve fresh=%v err=%v", fresh, err)
		}
		installExtensionAuditFailure(t, database)
		if err := database.MarkExtensionStored(ctx, result.Manifest.ID, result.Manifest.Version); err == nil {
			t.Fatal("extension stored state survived audit failure")
		}
		assertExtensionState(t, database, result.Manifest.ID, result.Manifest.Version, "staging", true)
	})

	t.Run("failed", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		result := extensionUploadAtomicFixture("extension-b")
		if _, _, err := database.ReserveExtensionPackage(
			ctx, result, "actor-b", "request-b", "/state/extension-b",
		); err != nil {
			t.Fatal(err)
		}
		installExtensionAuditFailure(t, database)
		if err := database.MarkExtensionFailed(
			ctx, result.Manifest.ID, result.Manifest.Version, "controlled failure",
		); err == nil {
			t.Fatal("extension failed state survived audit failure")
		}
		assertExtensionState(t, database, result.Manifest.ID, result.Manifest.Version, "staging", true)
	})
}

func extensionUploadAtomicFixture(id string) model.ExtensionUploadResult {
	return model.ExtensionUploadResult{
		Manifest: model.ExtensionManifest{
			ID: id, Version: "v1", Digest: "sha256:digest", Publisher: "publisher",
		},
		IdempotencyKey: "idem-" + id, StorageDigest: "sha256:digest",
		State: "staging", CreatedAt: time.Now().UTC(),
	}
}

func installExtensionAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_extension_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'extension.upload%' BEGIN SELECT RAISE(ABORT, 'forced extension audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropExtensionAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_extension_audit`); err != nil {
		t.Fatal(err)
	}
}

func assertExtensionState(
	t *testing.T,
	database *Store,
	id, version, want string,
	wantFound bool,
) {
	t.Helper()
	stored, found, err := extensionPackageByIdentity(context.Background(), database.db, id, version)
	if err != nil {
		t.Fatal(err)
	}
	if found != wantFound {
		t.Fatalf("extension found=%v, want %v", found, wantFound)
	}
	if found && stored.result.State != want {
		t.Fatalf("extension state=%s, want %s", stored.result.State, want)
	}
}
