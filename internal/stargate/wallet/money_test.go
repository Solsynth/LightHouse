package wallet

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestSplitEvenlyDistributesRemainder(t *testing.T) {
	amounts := splitEvenly(decimal.NewFromInt(10), 3)
	if len(amounts) != 3 {
		t.Fatalf("len = %d", len(amounts))
	}
	if amounts[0].String() != "3.34" || amounts[1].String() != "3.33" || amounts[2].String() != "3.33" {
		t.Fatalf("amounts = %v", amounts)
	}
	sum := decimal.Zero
	for _, amount := range amounts {
		sum = sum.Add(amount)
	}
	if !sum.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("sum = %s", sum)
	}
}

func TestSplitRandomlyKeepsTheTotal(t *testing.T) {
	total := decimal.NewFromInt(100)
	amounts := splitRandomly(total, 4)
	if len(amounts) != 4 {
		t.Fatalf("len = %d", len(amounts))
	}
	sum := decimal.Zero
	for _, amount := range amounts {
		if amount.IsNegative() {
			t.Fatalf("negative slice %s", amount)
		}
		sum = sum.Add(amount)
	}
	if !sum.Equal(total) {
		t.Fatalf("sum = %s; want %s", sum, total)
	}
}

func TestDynamicFundAmountShrinksWithClaims(t *testing.T) {
	fund := WalletFundEntity{
		IsOpen:          true,
		AmountOfSplits:  3,
		RemainingAmount: decimal.NewFromInt(9),
		SplitType:       FundSplitEven,
	}
	claimed := []WalletFundRecipientEntity{{IsReceived: true, Amount: decimal.NewFromInt(1)}}
	if got := dynamicFundAmount(&fund, claimed); got.String() != "4.5" {
		t.Fatalf("amount = %s; want 4.5", got)
	}
	fund.RemainingAmount = decimal.Zero
	if got := dynamicFundAmount(&fund, claimed); !got.IsZero() {
		t.Fatalf("exhausted fund amount = %s", got)
	}
}

func TestSubscriptionAvailabilityWindow(t *testing.T) {
	now := time.Now().UTC()
	active := WalletSubscriptionEntity{
		IsActive: true,
		Status:   SubscriptionActive,
		BegunAt:  now.Add(-time.Hour),
	}
	if !subscriptionAvailable(&active, now) || subscriptionPending(&active, now) {
		t.Fatal("active subscription not available")
	}
	ended := active
	ended.EndedAt = new(now.Add(-time.Minute))
	if subscriptionAvailable(&ended, now) {
		t.Fatal("ended subscription reported available")
	}
	lapsed := active
	lapsed.RenewalAt = new(now.Add(-time.Minute))
	if subscriptionAvailable(&lapsed, now) {
		t.Fatal("lapsed subscription reported available")
	}
	future := active
	future.BegunAt = now.Add(time.Hour)
	if subscriptionAvailable(&future, now) || !subscriptionPending(&future, now) {
		t.Fatal("future subscription window wrong")
	}
	cancelled := active
	cancelled.Status = SubscriptionCancelled
	if subscriptionAvailable(&cancelled, now) {
		t.Fatal("cancelled subscription reported available")
	}
}

func TestTruncateToThreeDecimals(t *testing.T) {
	amount, err := decimal.NewFromString("12.345678")
	if err != nil {
		t.Fatal(err)
	}
	if got := truncate3(amount); got.String() != "12.345" {
		t.Fatalf("truncated = %s", got)
	}
}

// Clients parse wallet amounts as JSON numbers. shopspring quotes
// decimal.Decimal by default, which the C# fleet never did.
func TestWalletDecimalsMarshalUnquoted(t *testing.T) {
	raw, err := json.Marshal(Wallet{
		ID: "wallet-1",
		Pockets: []Pocket{{
			ID:              "pocket-1",
			Currency:        "golds",
			Amount:          decimal.RequireFromString("12.50"),
			HeldAmount:      decimal.RequireFromString("0.25"),
			AvailableAmount: decimal.RequireFromString("12.25"),
		}},
	})
	if err != nil {
		t.Fatalf("marshal wallet: %v", err)
	}

	var decoded struct {
		Pockets []map[string]any `json:"pockets"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode wallet: %v", err)
	}
	if len(decoded.Pockets) != 1 {
		t.Fatalf("pockets = %d, want 1", len(decoded.Pockets))
	}
	for key, want := range map[string]float64{
		"amount":           12.50,
		"held_amount":      0.25,
		"available_amount": 12.25,
	} {
		got, ok := decoded.Pockets[0][key].(float64)
		if !ok || got != want {
			t.Errorf("%s = %#v (%T), want the number %v",
				key, decoded.Pockets[0][key], decoded.Pockets[0][key], want)
		}
	}
}
