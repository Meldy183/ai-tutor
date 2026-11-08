package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audioasset"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	competencypostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/application"
	sharedpostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupAudioRepository(t *testing.T) (*Repository, *pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL repository integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "task_audio_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := sharedpostgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ('subject:test','Тестовый предмет',1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return New(pool), pool, ctx
}

func TestFindReadyExcludesPreviousSynthesisVersion(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	_, err := pool.Exec(ctx, `INSERT INTO learning_audio_assets(id, instruction, object_key, status, bucket, storage_uri, audio_url)
		VALUES ('old', 'Повторная инструкция', 'task-audio/v1/old.wav', 'ready', 'test', 's3://test/old', '/old')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.FindReady(ctx, "Повторная инструкция", "new"); err != nil || found {
		t.Fatalf("old synthesis must not be reused: found=%v err=%v", found, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO learning_audio_assets(id, instruction, object_key, status, bucket, storage_uri, audio_url)
		VALUES ('current', 'Повторная инструкция', $1, 'ready', 'test', 's3://test/current', '/current')`, audioasset.ObjectKey("current"))
	if err != nil {
		t.Fatal(err)
	}
	asset, found, err := repository.FindReady(ctx, "Повторная инструкция", "new")
	if err != nil || !found || asset.ID != "current" {
		t.Fatalf("current synthesis must be reused: asset=%+v found=%v err=%v", asset, found, err)
	}
}

func seedCurrentTaskAudio(t *testing.T, pool *pgxpool.Pool, ctx context.Context, assetID, taskID string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO users(id,email,password_hash,created_at) VALUES ('audio-owner','audio-owner@example.test','test',1)", nil},
		{"INSERT INTO course_map_imports(revision,subject_id,imported_at,imported_by,competency_count,constituent_count,outcome_count,task_count,source_format,source_headers) VALUES (1,'subject:test',1,'audio-owner',1,1,1,1,'paired','[]')", nil},
		{"INSERT INTO course_map_competencies(id,name,revision) VALUES ('audio-competency','Audio',1)", nil},
		{"INSERT INTO course_map_constituents(id,competency_id,name) VALUES ('audio-constituent','audio-competency','Audio')", nil},
		{"INSERT INTO course_map_outcomes(id,constituent_id,name) VALUES ('audio-outcome','audio-constituent','Audio')", nil},
		{"INSERT INTO learning_audio_assets(id,instruction,object_key) VALUES ($1,'Speak answer',$2)", []any{assetID, "task-audio/v1/" + assetID + ".wav"}},
		{"INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,created_at,audio_asset_id) VALUES ($1,'audio-outcome','Question','Speak answer',1,$2)", []any{taskID, assetID}},
		{"INSERT INTO learning_subject_state(subject_id,active_revision) VALUES ('subject:test',1)", nil},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed current audio task: %v", err)
		}
	}
}

func TestClaimIsExclusiveAndStaleClaimCannotComplete(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-1", "task-1")
	first, ok, err := repository.Claim(ctx, "token-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	_, ok, err = repository.Claim(ctx, "token-2", time.Minute)
	if err != nil || ok {
		t.Fatalf("second claim: ok=%v err=%v, want no available claim", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET lease_until=now()-interval '1 second' WHERE id='asset-1'`); err != nil {
		t.Fatal(err)
	}
	second, ok, err := repository.Claim(ctx, "token-2", time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaimed expired lease: ok=%v err=%v", ok, err)
	}
	completed, err := repository.Complete(ctx, first, "bucket", "s3://bucket/key", "/task-audio/asset-1/file")
	if err != nil || completed {
		t.Fatalf("stale claim completed: completed=%v err=%v", completed, err)
	}
	completed, err = repository.Complete(ctx, second, "bucket", "s3://bucket/key", "/task-audio/asset-1/file")
	if err != nil || !completed {
		t.Fatalf("current claim completion: completed=%v err=%v", completed, err)
	}
	asset, err := repository.Get(ctx, "asset-1")
	if err != nil || asset.Status != "ready" || asset.AudioURL == nil {
		t.Fatalf("ready asset: %#v err=%v", asset, err)
	}
}

func TestPriorityMovesAheadWithoutInterruptingClaimsAndSurvivesRestart(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-a", "task-a")
	for _, id := range []string{"b", "c", "wanted"} {
		if _, err := pool.Exec(ctx, "INSERT INTO learning_audio_assets(id,instruction,object_key) VALUES ($1,'Speak answer',$1)", "asset-"+id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,created_at,audio_asset_id) VALUES ($1,'audio-outcome','Q','Speak answer',1,$2)", "task-"+id, "asset-"+id); err != nil {
			t.Fatal(err)
		}
	}
	first, ok, err := repository.Claim(ctx, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	second, ok, err := repository.Claim(ctx, "worker-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("second claim: ok=%v err=%v", ok, err)
	}
	if changed, err := repository.Prioritize(ctx, "asset-wanted"); err != nil || !changed {
		t.Fatalf("prioritize: changed=%v err=%v", changed, err)
	}
	for _, claim := range []application.Claim{first, second} {
		if current, err := repository.IsCurrent(ctx, claim); err != nil || !current {
			t.Fatalf("priority interrupted running TTS: asset=%s current=%v err=%v", claim.Asset.ID, current, err)
		}
	}
	if completed, err := repository.Complete(ctx, first, "bucket", "s3://bucket/key", "/task-audio/asset-a/file"); err != nil || !completed {
		t.Fatalf("background completion: completed=%v err=%v", completed, err)
	}
	restarted := New(pool)
	wanted, ok, err := restarted.Claim(ctx, "wanted-worker", time.Minute)
	if err != nil || !ok || wanted.Asset.ID != "asset-wanted" {
		t.Fatalf("priority lost on restart: claim=%#v ok=%v err=%v", wanted, ok, err)
	}
	if changed, err := restarted.Prioritize(ctx, wanted.Asset.ID); err != nil || changed {
		t.Fatalf("poll changed active recording: changed=%v err=%v", changed, err)
	}
	if current, err := restarted.IsCurrent(ctx, wanted); err != nil || !current {
		t.Fatalf("poll revoked wanted claim: current=%v err=%v", current, err)
	}
}

func TestRequestedWaitingRecordingMovesAheadOfANewerPriority(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-a", "task-a")
	if _, err := pool.Exec(ctx, "INSERT INTO learning_audio_assets(id,instruction,object_key) VALUES ('asset-b','Speak answer','b'); INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,created_at,audio_asset_id) VALUES ('task-b','audio-outcome','Q','Speak answer',1,'asset-b')"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"asset-a", "asset-b", "asset-a"} {
		if changed, err := repository.Prioritize(ctx, id); err != nil || !changed {
			t.Fatalf("request %s: changed=%v err=%v", id, changed, err)
		}
	}
	if changed, err := repository.Prioritize(ctx, "asset-a"); err != nil || changed {
		t.Fatalf("repeated front-of-queue request: changed=%v err=%v", changed, err)
	}
	first, ok, err := repository.Claim(ctx, "worker", time.Minute)
	if err != nil || !ok || first.Asset.ID != "asset-a" {
		t.Fatalf("requested recording not first: claim=%#v ok=%v err=%v", first, ok, err)
	}
}

func TestPriorityDoesNotInterruptWhenWorkerHasFreeCapacity(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-a", "task-a")
	if _, err := pool.Exec(ctx, "INSERT INTO learning_audio_assets(id,instruction,object_key) VALUES ('asset-wanted','Speak answer','wanted'); INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,created_at,audio_asset_id) VALUES ('task-wanted','audio-outcome','Q','Speak answer',1,'asset-wanted')"); err != nil {
		t.Fatal(err)
	}
	first, ok, err := repository.Claim(ctx, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if changed, err := repository.Prioritize(ctx, "asset-wanted"); err != nil || !changed {
		t.Fatalf("priority: changed=%v err=%v", changed, err)
	}
	if current, err := repository.IsCurrent(ctx, first); err != nil || !current {
		t.Fatalf("unused lane still caused cancellation: current=%v err=%v", current, err)
	}
}

func TestClaimRecoversInterruptedFinalAttemptAfterLeaseExpires(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-crashed", "task-crashed")
	if _, err := pool.Exec(ctx, "UPDATE learning_audio_assets SET attempts=4 WHERE id='asset-crashed'"); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := repository.Claim(ctx, "killed-worker", time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	restarted := New(pool)
	if _, ok, err := restarted.Claim(ctx, "restarted-worker", time.Minute); err != nil || ok {
		t.Fatalf("restart stole an unexpired claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE learning_audio_assets SET lease_until=now()-interval '1 second' WHERE id=$1", claim.Asset.ID); err != nil {
		t.Fatal(err)
	}
	next, ok, err := restarted.Claim(ctx, "restarted-worker", time.Minute)
	if err != nil || !ok || next.Asset.Attempts != 5 {
		t.Fatalf("crash exhausted the interrupted final attempt: claim=%#v ok=%v err=%v", next, ok, err)
	}
	if failed, err := restarted.Fail(ctx, next, "synthesis_failed", time.Second); err != nil || !failed {
		t.Fatalf("record final real failure: failed=%v err=%v", failed, err)
	}
	asset, err := restarted.Get(ctx, claim.Asset.ID)
	if err != nil || asset.Status != "failed" {
		t.Fatalf("five actual failures must still exhaust retries: asset=%#v err=%v", asset, err)
	}
}

func TestReleaseInterruptedClaimRequeuesImmediatelyWithoutConsumingAttempt(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-interrupted", "task-interrupted")
	if _, err := pool.Exec(ctx, "UPDATE learning_audio_assets SET attempts=4 WHERE id='asset-interrupted'"); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := repository.Claim(ctx, "old-worker", time.Hour)
	if err != nil || !ok || claim.Asset.Attempts != 5 {
		t.Fatalf("final claim: %#v ok=%v err=%v", claim, ok, err)
	}
	foreign := claim
	foreign.Token = "other-worker"
	if released, err := repository.Release(ctx, foreign); err != nil || released {
		t.Fatalf("foreign worker released a claim: released=%v err=%v", released, err)
	}
	if released, err := repository.Release(ctx, claim); err != nil || !released {
		t.Fatalf("shutdown did not release claim: released=%v err=%v", released, err)
	}
	asset, err := repository.Get(ctx, claim.Asset.ID)
	if err != nil || asset.Status != "pending" || asset.Attempts != 4 {
		t.Fatalf("interrupted attempt exhausted retries: asset=%#v err=%v", asset, err)
	}
	next, ok, err := New(pool).Claim(ctx, "new-worker", time.Minute)
	if err != nil || !ok || next.Asset.ID != claim.Asset.ID {
		t.Fatalf("restart still waits for old lease: claim=%#v ok=%v err=%v", next, ok, err)
	}
	if released, err := repository.Release(ctx, claim); err != nil || released {
		t.Fatalf("old worker released the new claim: released=%v err=%v", released, err)
	}
	if completed, err := repository.Complete(ctx, next, "bucket", "s3://bucket/key", "/task-audio/asset-interrupted/file"); err != nil || !completed {
		t.Fatalf("restarted completion: completed=%v err=%v", completed, err)
	}
	if released, err := repository.Release(ctx, next); err != nil || released {
		t.Fatalf("shutdown requeued completed recording: released=%v err=%v", released, err)
	}
}

func TestReleaseInterruptedClaimCancelsReplacedTask(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-replaced", "task-replaced")
	claim, ok, err := repository.Claim(ctx, "old-worker", time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE learning_subject_state SET active_revision=NULL WHERE subject_id='subject:test'"); err != nil {
		t.Fatal(err)
	}
	if released, err := repository.Release(ctx, claim); err != nil || !released {
		t.Fatalf("release stale task: released=%v err=%v", released, err)
	}
	asset, err := repository.Get(ctx, claim.Asset.ID)
	if err != nil || asset.Status != "cancelled" {
		t.Fatalf("replaced task was requeued: asset=%#v err=%v", asset, err)
	}
}

func TestClaimRepairSerializesReadyAssetsAndClearsReadyReferences(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-repair", "task-repair")
	initial, ok, err := repository.Claim(ctx, "token-initial", time.Minute)
	if err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}
	if completed, err := repository.Complete(ctx, initial, "legacy-bucket", "s3://legacy-bucket/key", "/task-audio/asset-repair/file"); err != nil || !completed {
		t.Fatalf("initial complete: completed=%v err=%v", completed, err)
	}
	repair, ok, err := repository.ClaimRepair(ctx, "asset-repair", "token-repair", time.Minute)
	if err != nil || !ok || repair.Token != "token-repair" {
		t.Fatalf("repair claim: claim=%+v ok=%v err=%v", repair, ok, err)
	}
	if repair.Asset.Bucket == nil || *repair.Asset.Bucket != "legacy-bucket" {
		t.Fatalf("repair claim lost previous bucket: %v", repair.Asset.Bucket)
	}
	var status string
	var audioURL, bucket *string
	if err := pool.QueryRow(ctx, `SELECT status,audio_url,bucket FROM learning_audio_assets WHERE id='asset-repair'`).Scan(&status, &audioURL, &bucket); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || audioURL != nil || bucket != nil {
		t.Fatalf("processing asset retains ready references: status=%s URL=%v bucket=%v", status, audioURL, bucket)
	}
	if _, ok, err := repository.ClaimRepair(ctx, "asset-repair", "second-repair", time.Minute); err != nil || ok {
		t.Fatalf("concurrent repair claim: ok=%v err=%v", ok, err)
	}
	if completed, err := repository.Complete(ctx, repair, "new-bucket", "s3://new-bucket/key", "/task-audio/asset-repair/file"); err != nil || !completed {
		t.Fatalf("repair complete: completed=%v err=%v", completed, err)
	}
	asset, err := repository.Get(ctx, "asset-repair")
	if err != nil || asset.Status != "ready" || asset.Bucket == nil || *asset.Bucket != "new-bucket" {
		t.Fatalf("repaired asset=%+v err=%v", asset, err)
	}
}

func TestClaimRepairRetriesFailedCurrentAsset(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-failed-repair", "task-failed-repair")
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET status='failed', attempts=5, last_error_code='synthesis_failed' WHERE id='asset-failed-repair'`); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := repository.ClaimRepair(ctx, "asset-failed-repair", "token-failed-repair", time.Minute)
	if err != nil || !ok || claim.Asset.Attempts != 1 {
		t.Fatalf("failed asset repair claim: claim=%+v ok=%v err=%v", claim, ok, err)
	}
	if _, ok, err := repository.ClaimRepair(ctx, "asset-failed-repair", "second-repair", time.Minute); err != nil || ok {
		t.Fatalf("concurrent failed asset repair claim: ok=%v err=%v", ok, err)
	}
}

func TestClaimRepairRejectsPendingAndStaleSnapshotOnlyAssets(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-pending-repair", "task-pending-repair")
	if _, ok, err := repository.ClaimRepair(ctx, "asset-pending-repair", "token-pending", time.Minute); err != nil || ok {
		t.Fatalf("pending asset repair claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET status='ready',bucket='bucket',storage_uri='s3://bucket/key',audio_url='/task-audio/asset-pending-repair/file' WHERE id='asset-pending-repair'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_subject_state SET active_revision=NULL WHERE subject_id='subject:test'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repository.ClaimRepair(ctx, "asset-pending-repair", "token-stale", time.Minute); err != nil || ok {
		t.Fatalf("stale asset repair claim: ok=%v err=%v", ok, err)
	}
}

func TestClaimRepairResetsOldAttemptsSoExpiredRepairRecovers(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-repair-expired", "task-repair-expired")
	initial, ok, err := repository.Claim(ctx, "token-initial-expired", time.Minute)
	if err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}
	if completed, err := repository.Complete(ctx, initial, "bucket", "s3://bucket/key", "/task-audio/asset-repair-expired/file"); err != nil || !completed {
		t.Fatalf("initial complete: completed=%v err=%v", completed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET attempts=5 WHERE id='asset-repair-expired'`); err != nil {
		t.Fatal(err)
	}
	repair, ok, err := repository.ClaimRepair(ctx, "asset-repair-expired", "token-repair-expired", time.Minute)
	if err != nil || !ok || repair.Asset.Attempts != 1 {
		t.Fatalf("repair claim attempts=%d ok=%v err=%v", repair.Asset.Attempts, ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET lease_until=now()-interval '1 second' WHERE id='asset-repair-expired'`); err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := repository.Claim(ctx, "token-recovered-repair", time.Minute)
	if err != nil || !ok || recovered.Asset.Status != "processing" || recovered.Asset.Attempts != 1 {
		t.Fatalf("expired repair did not recover: asset=%+v ok=%v err=%v", recovered.Asset, ok, err)
	}
}

func TestClaimRejectsStaleTaskAndExpiryCancelsIt(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-stale", "task-stale")
	claim, ok, err := repository.Claim(ctx, "token-stale", time.Minute)
	if err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_subject_state SET active_revision=NULL WHERE subject_id='subject:test'`); err != nil {
		t.Fatal(err)
	}
	if current, err := repository.IsCurrent(ctx, claim); err != nil || current {
		t.Fatalf("stale claim current=%v err=%v", current, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET lease_until=now()-interval '1 second' WHERE id='asset-stale'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repository.Claim(ctx, "token-next", time.Minute); err != nil || ok {
		t.Fatalf("stale asset claimed after recovery: ok=%v err=%v", ok, err)
	}
	asset, err := repository.Get(ctx, "asset-stale")
	if err != nil || asset.Status != "cancelled" {
		t.Fatalf("expired stale asset status=%q err=%v", asset.Status, err)
	}
	if cancelled, err := repository.Cancel(ctx, claim); err != nil || cancelled {
		t.Fatalf("expired worker cancelled after lease recovery: cancelled=%v err=%v", cancelled, err)
	}
}

func TestFailureAfterReplacementCancelsInsteadOfRequeueing(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-replaced", "task-replaced")
	claim, ok, err := repository.Claim(ctx, "token-replaced", time.Minute)
	if err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_subject_state SET active_revision=NULL WHERE subject_id='subject:test'`); err != nil {
		t.Fatal(err)
	}
	if failed, err := repository.Fail(ctx, claim, "synthesis_failed", time.Second); err != nil || !failed {
		t.Fatalf("record failed stale attempt: failed=%v err=%v", failed, err)
	}
	asset, err := repository.Get(ctx, "asset-replaced")
	if err != nil || asset.Status != "cancelled" {
		t.Fatalf("stale failure requeued asset: status=%q err=%v", asset.Status, err)
	}
	if _, ok, err := repository.Claim(ctx, "token-retry", time.Minute); err != nil || ok {
		t.Fatalf("cancelled stale asset was claimed: ok=%v err=%v", ok, err)
	}
}

func TestExpiredLeaseOnCurrentTaskIsRecoverableNotCancelled(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	seedCurrentTaskAudio(t, pool, ctx, "asset-current-expired", "task-current-expired")
	claim, ok, err := repository.Claim(ctx, "token-expired", time.Minute)
	if err != nil || !ok {
		t.Fatalf("initial claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET lease_until=now()-interval '1 second' WHERE id='asset-current-expired'`); err != nil {
		t.Fatal(err)
	}
	if current, err := repository.IsCurrent(ctx, claim); err != nil || current {
		t.Fatalf("expired lease check: current=%v err=%v", current, err)
	}
	if cancelled, err := repository.Cancel(ctx, claim); err != nil || cancelled {
		t.Fatalf("active map task was cancelled for expired lease: cancelled=%v err=%v", cancelled, err)
	}
	reclaimed, ok, err := repository.Claim(ctx, "token-reclaimed", time.Minute)
	if err != nil || !ok || reclaimed.Token != "token-reclaimed" {
		t.Fatalf("current task was not recoverable: claim=%+v ok=%v err=%v", reclaimed, ok, err)
	}
}

func TestAudioQueueUsesEachSubjectsActiveRevisionAcrossImportAndRepair(t *testing.T) {
	repository, pool, ctx := setupAudioRepository(t)
	const ownerID = "audio-owner"
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,created_at,role) VALUES ($1,'audio-owner@example.test','hash',1,'admin')`, ownerID); err != nil {
		t.Fatal(err)
	}
	maps := competencypostgres.New(pool)
	if _, err := maps.Replace(ctx, "subject:test", ownerID, audioMap("A", 2), 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ('subject:b','Subject B',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := maps.Replace(ctx, "subject:b", ownerID, audioMap("B", 1), 2, nil); err != nil {
		t.Fatal(err)
	}
	assets := map[string][]string{}
	taskByAsset := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT subject.id, task.id, task.audio_asset_id
FROM course_map_tasks task JOIN course_map_outcomes outcome ON outcome.id=task.outcome_id
JOIN course_map_constituents constituent ON constituent.id=outcome.constituent_id
JOIN course_map_competencies competency ON competency.id=constituent.competency_id
JOIN subjects subject ON true
JOIN learning_subject_state state ON state.subject_id=subject.id AND state.active_revision=competency.revision
ORDER BY subject.id, task.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var subjectID, taskID string
		var assetID *string
		if err := rows.Scan(&subjectID, &taskID, &assetID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if assetID == nil {
			rows.Close()
			t.Fatalf("task %s has no pending audio asset", taskID)
		}
		assets[subjectID] = append(assets[subjectID], *assetID)
		taskByAsset[*assetID] = taskID
	}
	rows.Close()
	if len(assets["subject:test"]) != 2 || len(assets["subject:b"]) != 1 {
		t.Fatalf("active task assets by subject = %#v", assets)
	}
	claimed := make(map[string][]application.Claim)
	for range 3 {
		claim, ok, err := repository.Claim(ctx, "subject-claim", time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim across active subjects: ok=%v err=%v", ok, err)
		}
		var subjectID string
		if err := pool.QueryRow(ctx, `SELECT subject.id FROM course_map_tasks task JOIN course_map_outcomes outcome ON outcome.id=task.outcome_id JOIN course_map_constituents constituent ON constituent.id=outcome.constituent_id JOIN course_map_competencies competency ON competency.id=constituent.competency_id JOIN subjects subject ON true
JOIN learning_subject_state state ON state.subject_id=subject.id AND state.active_revision=competency.revision WHERE task.audio_asset_id=$1`, claim.Asset.ID).Scan(&subjectID); err != nil {
			t.Fatal(err)
		}
		claimed[subjectID] = append(claimed[subjectID], claim)
	}
	if len(claimed["subject:test"]) != 2 || len(claimed["subject:b"]) != 1 {
		t.Fatalf("claimed audio by active subject = %#v", claimed)
	}
	claimAProcessing, claimAPending, claimB := claimed["subject:test"][0], claimed["subject:test"][1], claimed["subject:b"][0]
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET status='processing',claim_token=$2,lease_until=now()+interval '1 hour' WHERE id=$1`, claimAProcessing.Asset.ID, claimAProcessing.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET status='pending',claim_token=NULL,lease_until=NULL WHERE id=$1`, claimAPending.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET status='processing',claim_token=$2,lease_until=now()-interval '1 second' WHERE id=$1`, claimB.Asset.ID, claimB.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning_variants(id,user_id,create_request_key,map_revision,algorithm_version,included_competency_count,skipped_competencies,created_at,subject_id,subject_name_snapshot) SELECT 'historical-audio','audio-owner','historical-audio',state.active_revision,'test',1,'[]',1,subject.id,subject.name FROM subjects subject JOIN learning_subject_state state ON state.subject_id=subject.id WHERE subject.id=$1`, "subject:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning_variant_tasks(id,variant_id,live_task_id,source_task_id_snapshot,audio_asset_id,competency_position,slot,role,task_snapshot,profile_snapshot) VALUES ('historical-audio-task','historical-audio',$1,$1,$2,1,0,'main','{"question":"Old A task"}','{}')`, taskByAsset[claimAProcessing.Asset.ID], claimAProcessing.Asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := maps.Replace(ctx, "subject:test", ownerID, audioMap("A replacement", 1), 3, nil); err != nil {
		t.Fatal(err)
	}
	var staleAStatus, bStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM learning_audio_assets WHERE id=$1`, claimAPending.Asset.ID).Scan(&staleAStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM learning_audio_assets WHERE id=$1`, claimB.Asset.ID).Scan(&bStatus); err != nil {
		t.Fatal(err)
	}
	if staleAStatus != "cancelled" || bStatus != "processing" {
		t.Fatalf("import A statuses: old A pending=%q, B expired processing=%q", staleAStatus, bStatus)
	}
	if current, err := repository.IsCurrent(ctx, claimAProcessing); err != nil || current {
		t.Fatalf("processing A asset stayed current: current=%v err=%v", current, err)
	}
	if cancelled, err := repository.Cancel(ctx, claimAProcessing); err != nil || !cancelled {
		t.Fatalf("stale processing A asset cancel: cancelled=%v err=%v", cancelled, err)
	}
	// Ignore newly imported A work so the next claim proves expired B work is recovered.
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets asset SET status='cancelled' FROM course_map_tasks task WHERE task.audio_asset_id=asset.id AND task.id IN (SELECT task.id FROM course_map_tasks task JOIN course_map_outcomes outcome ON outcome.id=task.outcome_id JOIN course_map_constituents constituent ON constituent.id=outcome.constituent_id JOIN course_map_competencies competency ON competency.id=constituent.competency_id JOIN subjects subject ON subject.id=$1 JOIN learning_subject_state state ON state.subject_id=subject.id AND state.active_revision=competency.revision)`, "subject:test"); err != nil {
		t.Fatal(err)
	}
	recoveredB, ok, err := repository.Claim(ctx, "recovered-b", time.Minute)
	if err != nil || !ok || recoveredB.Asset.ID != claimB.Asset.ID {
		t.Fatalf("expired B asset recovery: claim=%+v ok=%v err=%v", recoveredB, ok, err)
	}
	if completed, err := repository.Complete(ctx, recoveredB, "bucket", "s3://bucket/b.wav", "/task-audio/b/file"); err != nil || !completed {
		t.Fatalf("complete recovered B asset: completed=%v err=%v", completed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning_audio_assets SET status='ready',bucket='old-bucket',storage_uri='s3://old-bucket/a.wav',audio_url='/task-audio/a/file' WHERE id=$1`, claimAProcessing.Asset.ID); err != nil {
		t.Fatal(err)
	}
	repairB, ok, err := repository.ClaimRepair(ctx, claimB.Asset.ID, "repair-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("B asset was not repairable after A import: ok=%v err=%v", ok, err)
	}
	if _, ok, err := repository.ClaimRepair(ctx, claimAProcessing.Asset.ID, "repair-stale-a", time.Minute); err != nil || ok {
		t.Fatalf("stale A asset remained repairable: ok=%v err=%v", ok, err)
	}
	if accessible, err := repository.GetAccessible(ctx, ownerID, claimAProcessing.Asset.ID); err != nil || accessible.ID != claimAProcessing.Asset.ID {
		t.Fatalf("historical variant audio access after A reimport: asset=%+v err=%v", accessible, err)
	}
	if completed, err := repository.Complete(ctx, repairB, "bucket", "s3://bucket/b-repaired.wav", "/task-audio/b/repaired"); err != nil || !completed {
		t.Fatalf("complete B repair: completed=%v err=%v", completed, err)
	}
}

func audioMap(name string, taskCount int) competencymap.Map {
	data := competencymap.Map{
		SourceFormat: "paired", SourceHeaders: []string{"Competency", "Constituent", "Outcome"},
		Competencies: []competencymap.Competency{{Key: "c", Name: "Competency " + name}},
		Constituents: []competencymap.Constituent{{Key: "s", CompetencyKey: "c", Name: "Constituent " + name}},
		Outcomes:     []competencymap.Outcome{{Key: "o", ConstituentKey: "s", Name: "Outcome " + name}},
		SourceRows:   []competencymap.SourceRow{{Index: 1, Line: 2, Cells: []string{"Competency " + name, "Constituent " + name, "Outcome " + name}}},
	}
	for index := range taskCount {
		data.Tasks = append(data.Tasks, competencymap.Task{OutcomeKey: "o", Question: fmt.Sprintf("Question %s %d", name, index+1), VoiceInstruction: "Speak answer", ReferenceAnswer: "Answer", Options: []string{"A", "B"}, Column: "Outcome", Row: 2, SourceRowIndex: 1, SourceColumnIndex: 3})
	}
	return data
}
