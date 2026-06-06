package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/exbanka/contract/identity"
	pb "github.com/exbanka/contract/transactionpb"
	"github.com/exbanka/transaction-service/internal/model"
)

func ctxAs(c identity.Caller) context.Context {
	md, _ := metadata.FromOutgoingContext(identity.Inject(context.Background(), c))
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestOWN1_GetPayment_ClientForeign_NotFound(t *testing.T) {
	h := newTestHandler(func(pm *mockPaymentFacade) {
		pm.getPaymentFn = func(id uint64) (*model.Payment, error) {
			return &model.Payment{ID: id, ClientID: 9}, nil
		}
	}, nil, nil)
	ctx := ctxAs(identity.Caller{PrincipalType: identity.PrincipalClient, PrincipalID: 5})
	_, err := h.GetPayment(ctx, &pb.GetPaymentRequest{Id: 1})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestOWN1_GetPayment_ClientOwn_OK(t *testing.T) {
	h := newTestHandler(func(pm *mockPaymentFacade) {
		pm.getPaymentFn = func(id uint64) (*model.Payment, error) {
			return &model.Payment{ID: id, ClientID: 5}, nil
		}
	}, nil, nil)
	ctx := ctxAs(identity.Caller{PrincipalType: identity.PrincipalClient, PrincipalID: 5})
	_, err := h.GetPayment(ctx, &pb.GetPaymentRequest{Id: 1})
	require.NoError(t, err)
}

func TestOWN1_GetPayment_Employee_OK(t *testing.T) {
	h := newTestHandler(func(pm *mockPaymentFacade) {
		pm.getPaymentFn = func(id uint64) (*model.Payment, error) {
			return &model.Payment{ID: id, ClientID: 9}, nil
		}
	}, nil, nil)
	ctx := ctxAs(identity.Caller{PrincipalType: identity.PrincipalEmployee, PrincipalID: 7})
	_, err := h.GetPayment(ctx, &pb.GetPaymentRequest{Id: 1})
	require.NoError(t, err)
}

func TestOWN1_GetTransfer_ClientForeign_NotFound(t *testing.T) {
	h := newTestHandler(nil, func(tm *mockTransferFacade) {
		tm.getTransferFn = func(id uint64) (*model.Transfer, error) {
			return &model.Transfer{ID: id, ClientID: 9}, nil
		}
	}, nil)
	ctx := ctxAs(identity.Caller{PrincipalType: identity.PrincipalClient, PrincipalID: 5})
	_, err := h.GetTransfer(ctx, &pb.GetTransferRequest{Id: 1})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestOWN1_GetTransferStatus_ClientForeign_NotFound(t *testing.T) {
	h := newTestHandler(nil, func(tm *mockTransferFacade) {
		tm.getTransferFn = func(id uint64) (*model.Transfer, error) {
			return &model.Transfer{ID: id, ClientID: 9}, nil
		}
	}, nil)
	ctx := ctxAs(identity.Caller{PrincipalType: identity.PrincipalClient, PrincipalID: 5})
	_, err := h.GetTransferStatus(ctx, &pb.GetTransferRequest{Id: 1})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
