package realm

import (
	"context"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

// Payments opens the wallet order a realm boost is charged on. The local
// wallet module and the remote wallet adapter both provide it as a port.
type Payments interface {
	CreateOrder(ctx context.Context, req OrderInput) (*OrderResult, error)
}

// OrderInput is the order a boost purchase creates. It mirrors the fields the
// C# RemotePaymentService.CreateOrder call passed.
type OrderInput struct {
	AccountID         string
	Currency          string
	Amount            Decimal
	ProductIdentifier string
	AppIdentifier     string
	Remarks           string
	Meta              map[string]any
}

// OrderResult is the opened order.
type OrderResult struct {
	ID string
}

// FileResolver resolves a cloud-file id into its reference object, for realm
// pictures and backgrounds. It is satisfied by ports.Files.
type FileResolver interface {
	Resolve(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error)
}

// BlockChecker answers whether the inviting account is blocked by the invitee.
// It is satisfied by *relationship.Service.
type BlockChecker interface {
	HasStatus(ctx context.Context, accountID, relatedID uuid.UUID, status model.RelationshipStatus) (bool, error)
}

// ProfileLookup reloads an account with its profile, which carries the level
// the realm quota is built from. It is satisfied by *store.Store.
type ProfileLookup interface {
	GetAccountWithProfile(ctx context.Context, id uuid.UUID) (*model.Account, error)
}
