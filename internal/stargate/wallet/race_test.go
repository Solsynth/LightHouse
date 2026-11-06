package wallet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/stargate/dbtest"
)

const walletSmokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func newWalletSmokeDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	dsn, cleanup, err := dbtest.NewDatabase(ctx, walletSmokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(SchemaModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestWalletConcurrentDebitCannotOverdraw(t *testing.T) {
	db := newWalletSmokeDB(t)
	svc := NewService(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ctx := context.Background()

	payerID := newID()
	if err := db.Create(&WalletEntity{ID: payerID, Name: "payer"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&WalletPocketEntity{ID: newID(), WalletID: payerID, Currency: "pts",
		Amount: decimal.NewFromInt(100), HeldAmount: decimal.Zero}).Error; err != nil {
		t.Fatal(err)
	}

	var okCount atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateTransaction(ctx, TransactionInput{
				PayerWalletID: &payerID,
				Currency:      "pts",
				Amount:        decimal.NewFromInt(100),
				Type:          TransactionTransfer,
			})
			switch {
			case err == nil:
				okCount.Add(1)
			case errors.Is(err, ErrInsufficientFunds):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := okCount.Load(); got != 1 {
		t.Fatalf("concurrent debits: %d succeeded, want exactly 1", got)
	}
	var pocket WalletPocketEntity
	if err := db.Where("wallet_id = ? AND currency = ?", payerID, "pts").First(&pocket).Error; err != nil {
		t.Fatal(err)
	}
	if !pocket.Amount.Equal(decimal.Zero) {
		t.Fatalf("pocket amount = %s, want 0", pocket.Amount)
	}
}

func TestWalletConcurrentOrderPayChargesOnce(t *testing.T) {
	db := newWalletSmokeDB(t)
	svc := NewService(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ctx := context.Background()

	payerID := newID()
	payeeID := newID()
	accountID := uuid.New()
	if err := db.Create(&WalletEntity{ID: payerID, AccountID: &accountID, Name: "payer"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&WalletEntity{ID: payeeID, Name: "payee"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&WalletPocketEntity{ID: newID(), WalletID: payerID, Currency: "pts",
		Amount: decimal.NewFromInt(100), HeldAmount: decimal.Zero}).Error; err != nil {
		t.Fatal(err)
	}
	order := &WalletOrderEntity{
		ID:            newID(),
		Status:        OrderUnpaid,
		Currency:      "pts",
		Amount:        decimal.NewFromInt(40),
		PayeeWalletID: &payeeID,
		ExpiredAt:     time.Now().UTC().Add(time.Hour),
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	payer := &WalletEntity{ID: payerID, AccountID: &accountID, Name: "payer"}

	var okCount atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.PayOrder(ctx, order.ID, payer); err == nil {
				okCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := okCount.Load(); got < 1 {
		t.Fatalf("concurrent order pays: %d succeeded, want at least 1", got)
	}
	var txCount int64
	if err := db.Model(&WalletTransactionEntity{}).
		Where("payer_wallet_id = ?", payerID).Count(&txCount).Error; err != nil {
		t.Fatal(err)
	}
	if txCount != 1 {
		t.Fatalf("order debits = %d, want exactly 1", txCount)
	}
	var pocket WalletPocketEntity
	if err := db.Where("wallet_id = ? AND currency = ?", payerID, "pts").First(&pocket).Error; err != nil {
		t.Fatal(err)
	}
	if !pocket.Amount.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("payer amount = %s, want 60", pocket.Amount)
	}
	var stored WalletOrderEntity
	if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != OrderPaid || stored.TransactionID == nil {
		t.Fatalf("order state = status %d tx %v, want paid with a transaction", stored.Status, stored.TransactionID)
	}
}
