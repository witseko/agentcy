package conversation

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

func TestFinishListenFlushesLedgerBeforeStatus(t *testing.T) {
	dataDir := t.TempDir()
	tokensConfig := `{"models":{"test-model":{"budget":10000}}}`
	if err := os.WriteFile(filepath.Join(dataDir, "tokens.json"), []byte(tokensConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	shared := newTestShared(t, dataDir, log.New(io.Discard, "", 0))
	handler := NewHandler(shared, nil, nil)
	ctx := context.Background()
	now := time.Unix(startUnix, 0)

	model := "test-model"
	created, err := handler.Member(ctx, &wire.Create{
		Workspace:      testWorkspace,
		Brief:          "A brief with enough words to estimate tokens when delivered to a listener.",
		Initiator:      "Lead",
		Invitees:       []wire.Slot{{Name: "Worker"}},
		InitiatorModel: &model,
	}, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	leadToken := text(created.Result, "token")
	workerCode := text(created.Result, "invitations", "Worker")

	joined, err := handler.Member(ctx, &wire.Join{
		Workspace: testWorkspace,
		Code:      workerCode,
		Model:     &model,
	}, now)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	workerToken := text(joined.Result, "token")

	if _, err := handler.Member(ctx, &wire.Post{
		Workspace:    testWorkspace,
		Token:        leadToken,
		SubmissionID: "sub-1",
		Body:         "Please review this long message body so read delivery records non-zero input tokens in the ledger.",
		Kind:         "note",
	}, now); err != nil {
		t.Fatalf("post: %v", err)
	}

	readReq := &wire.Read{Workspace: testWorkspace, Token: workerToken, After: 0, WaitSeconds: 1}
	waitPage, err := handler.ReadPage(ctx, readReq, now)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if !waitPage.Ready || !waitPage.HasMessages {
		t.Fatalf("expected ready page with messages")
	}

	listenStatus, err := handler.FinishListen(ctx, readReq, waitPage.Page, true, now)
	if err != nil {
		t.Fatalf("FinishListen: %v", err)
	}

	freshStatus, err := handler.Member(ctx, &wire.Status{
		Workspace: testWorkspace,
		Token:     workerToken,
	}, now)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	listenModelUsed := field(listenStatus, "members", "Worker", "usage", "model_tokens_used")
	freshModelUsed := field(freshStatus.Result, "members", "Worker", "usage", "model_tokens_used")
	if used, ok := listenModelUsed.IntOf(); !ok || used <= 0 {
		t.Fatalf("FinishListen model_tokens_used = %s, want > 0", listenModelUsed.Compact())
	}
	if listenModelUsed.Compact() != freshModelUsed.Compact() {
		t.Fatalf("FinishListen model_tokens_used = %s, subsequent Status = %s", listenModelUsed.Compact(), freshModelUsed.Compact())
	}

	listenBalance := field(listenStatus, "token_balance")
	freshBalance := field(freshStatus.Result, "token_balance")
	if listenBalance.Compact() != freshBalance.Compact() {
		t.Fatalf("FinishListen token_balance = %s, subsequent Status = %s", listenBalance.Compact(), freshBalance.Compact())
	}
}

func TestFinishListenRefreshesParticipantWithoutDelivery(t *testing.T) {
	shared := newTestShared(t, t.TempDir(), log.New(io.Discard, "", 0))
	handler := NewHandler(shared, nil, nil)
	ctx := context.Background()
	now := time.Unix(startUnix, 0)

	minutes := int64(1)
	created, err := handler.Member(ctx, &wire.Create{
		Workspace:       testWorkspace,
		Brief:           "Test brief",
		Initiator:       "Lead",
		Invitees:        []wire.Slot{{Name: "Worker"}},
		Sealed:          true,
		DeadlineMinutes: &minutes,
	}, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	workerCode := text(created.Result, "invitations", "Worker")

	joined, err := handler.Member(ctx, &wire.Join{
		Workspace: testWorkspace,
		Code:      workerCode,
	}, now)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	workerToken := text(joined.Result, "token")

	// After the deadline passes, FinishListen with deliver=false must still release the sealed round.
	afterDeadline := now.Add(2 * time.Minute)
	readReq := &wire.Read{Workspace: testWorkspace, Token: workerToken, After: 0, WaitSeconds: 1}
	statusValue, err := handler.FinishListen(ctx, readReq, wire.Value{}, false, afterDeadline)
	if err != nil {
		t.Fatalf("FinishListen(deliver=false): %v", err)
	}
	if got := text(statusValue, "round"); got != roundCritique {
		t.Fatalf("round = %q, want %q after sealed deadline", got, roundCritique)
	}
}
