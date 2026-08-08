package queue

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

func TestEnqueuePushNotificationRequiresNATS(t *testing.T) {
	svc := New(nil, 1, slog.Default())
	n := &model.Notification{ModelBase: model.ModelBase{Id: uuid.New()}, AccountId: uuid.New()}

	if err := svc.EnqueuePushNotification(context.Background(), n, n.AccountId, nil, true); err == nil {
		t.Fatal("enqueue should fail when NATS is unavailable")
	}
}
