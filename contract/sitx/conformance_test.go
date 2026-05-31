package sitx

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"
)

// dn is a small helper to build a DecimalNumber wire amount from a string.
func dn(s string) DecimalNumber {
	return DecimalNumber{Decimal: decimal.RequireFromString(s)}
}

// coffeePosting2 is the §2.8 coffee transaction's credit leg (the receiving
// account). It is reused by the vote_no fixture's NoVoteReason.
func coffeePosting2() Posting {
	return Posting{
		Account: TxAccount{Type: AccountTypeAccount, Num: "111000141215476411"},
		Amount:  dn("260"),
		Asset:   Asset{Type: AssetTypeMonas, Asset: MonetaryAsset{Currency: "RSD"}},
	}
}

// TestConformance asserts that marshalling each canonical Go value produces
// bytes that are JSON-equal (after json.Compact) to the hand-authored spec
// fixture under testdata/. These fixtures are the shared cohort-interop target:
// any team can diff their encoder output against them. The Go encoder is the
// source of truth for field ORDER; fixtures are authored in struct-field order.
func TestConformance(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		value   any
	}{
		{
			name:    "newtx_coffee",
			fixture: "newtx_coffee.json",
			value: Message[Transaction]{
				IdempotenceKey: IdempotenceKey{RoutingNumber: 111, LocallyGeneratedKey: "k-coffee-1"},
				MessageType:    MessageTypeNewTx,
				Message: Transaction{
					Postings: []Posting{
						{
							Account: TxAccount{Type: AccountTypeAccount, Num: "444000100182503611"},
							Amount:  dn("-260"),
							Asset:   Asset{Type: AssetTypeMonas, Asset: MonetaryAsset{Currency: "RSD"}},
						},
						coffeePosting2(),
					},
					TransactionID:  ForeignBankId{RoutingNumber: 111, ID: "k-coffee-1"},
					Message:        "coffee",
					PaymentCode:    "289",
					PaymentPurpose: "debt",
				},
			},
		},
		{
			name:    "vote_no",
			fixture: "vote_no.json",
			value: func() TransactionVote {
				p := coffeePosting2()
				return TransactionVote{
					Vote: VoteNo,
					Reasons: []NoVoteReason{
						{Reason: NoVoteReasonInsufficientAsset, Posting: &p},
					},
				}
			}(),
		},
		{
			name:    "public_stock",
			fixture: "public_stock.json",
			value: PublicStocksResponse{
				{
					Stock: StockDescription{Ticker: "AAPL"},
					Sellers: []PublicSeller{
						{Seller: ForeignBankId{RoutingNumber: 111, ID: "client-3"}, Amount: 50},
						{Seller: ForeignBankId{RoutingNumber: 111, ID: "client-9"}, Amount: 20},
					},
				},
			},
		},
		{
			// UserInformation's Go struct is {id, firstName, lastName}, which does
			// NOT match the spec §3.7 shape. The gateway /user handler emits the
			// spec shape via gin.H, so the fixture reflects {bankDisplayName,
			// displayName} built from a map rather than UserInformation.
			name:    "user",
			fixture: "user.json",
			value: map[string]string{
				"bankDisplayName": "EXBanka",
				"displayName":     "Marko Marković",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			gotCompact := compact(t, got)

			want, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("read fixture %s: %v", tc.fixture, err)
			}
			wantCompact := compact(t, want)

			if !bytes.Equal(gotCompact, wantCompact) {
				t.Errorf("fixture %s mismatch:\n got: %s\nwant: %s", tc.fixture, gotCompact, wantCompact)
			}
		})
	}
}

func compact(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("compact: %v\ninput: %s", err, b)
	}
	return buf.Bytes()
}
