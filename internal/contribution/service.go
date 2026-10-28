package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/loan"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

type Service struct {
	store        *Store
	shareService *share.Service
	loanService  *loan.Service
}

func NewService(store *Store, shareService *share.Service, loanService *loan.Service) *Service {
	return &Service{
		store:        store,
		shareService: shareService,
		loanService:  loanService,
	}
}

type AllocationInput struct {
	Type     contributionsqlc.ContributionAllocationType
	TargetID pgtype.UUID
	Amount   pgtype.Numeric
	// DisplayLabel snapshots charge wording so later edits cannot change cash reprints.
	DisplayLabel string
}

type CreatedReceipt struct {
	Receipt     contributionsqlc.ContributionReceipt
	Allocations []contributionsqlc.ContributionAllocation
}

// CreateReceipt persists the received money and its allocation rows in one
// transaction. The final spec says a contribution receipt is complete only
// after every authoritative allocation succeeds, so this first step must never
// leave a receipt without the rows that future processing will retry or audit.
func (s *Service) CreateReceipt(
	ctx context.Context,
	params contributionsqlc.InsertContributionReceiptParams,
	allocations []AllocationInput,
) (CreatedReceipt, error) {
	var result CreatedReceipt
	err := s.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		q := contributionsqlc.New(tx)
		existing, err := existingReceipt(ctx, q, params)
		if err == nil {
			allocations, err = allocationsForRetry(allocations, existing.AllocationPlan)
		} else if errors.Is(err, ErrReceiptNotFound) {
			var fees []AllocationInput
			fees, err = monthlyFees(ctx, q, params.MemberID, params.ContributionPeriod, pgtype.UUID{})
			if err == nil {
				allocations, err = calculatedAllocations(allocations, fees)
			}
		}
		if err != nil {
			return err
		}
		params.AllocationPlan, err = buildAllocationPlan(allocations)
		if err != nil {
			return err
		}
		if err := validateReceipt(params, allocations); err != nil {
			return err
		}
		if !existing.ID.Valid {
			if err := validateChargeAllocations(ctx, q, params.MemberID, params.BranchID, allocations, pgtype.UUID{}); err != nil {
				return err
			}
			if err := validateContributionLoanMinimums(ctx, loansqlc.New(tx), params.MemberID, params.BranchID, params.ContributionPeriod, allocations); err != nil {
				return err
			}
		}
		created, err := s.createReceipt(ctx, q, params, allocations)
		result = created
		return err
	})
	if err != nil {
		return CreatedReceipt{}, err
	}
	return result, nil
}

// validateContributionLoanMinimums locks every payable loan, including omitted
// allocations, so cash and STK cannot bypass an unpaid installment minimum.
func validateContributionLoanMinimums(ctx context.Context, q loansqlc.Querier, memberID pgtype.UUID, branchID int64, period pgtype.Date, allocations []AllocationInput) error {
	if _, err := q.LockMemberContributionLoans(ctx, loansqlc.LockMemberContributionLoansParams{MemberID: memberID, BranchID: branchID}); err != nil {
		return err
	}
	positions, err := loan.QuoteContributionLoans(ctx, q, memberID, branchID, period)
	if err != nil {
		return err
	}
	for _, position := range positions {
		var due pgtype.Numeric
		if err := due.Scan(position.InstallmentDue); err != nil {
			return err
		}
		payment := new(big.Int)
		for _, allocation := range allocations {
			if allocation.Type == contributionsqlc.ContributionAllocationTypeLoanPrincipal && allocation.TargetID.String() == position.LoanID {
				payment.Add(payment, numericToScale(allocation.Amount, -4))
			}
		}
		if payment.Cmp(numericToScale(due, -4)) < 0 {
			return fmt.Errorf("%w: loan %s requires at least KES %s for %s", ErrInvalidAllocation, position.LoanID, position.InstallmentDue, period.Time.Format("2006-01"))
		}
	}
	return nil
}

// createReceipt is the transaction-scoped body shared by manual receipt entry
// and Daraja callback processing. Removing it would duplicate the idempotent
// receipt/allocation insert logic and risk one path diverging from the other.
func (s *Service) createReceipt(
	ctx context.Context,
	q contributionsqlc.Querier,
	params contributionsqlc.InsertContributionReceiptParams,
	allocations []AllocationInput,
) (CreatedReceipt, error) {
	receipt, err := q.InsertContributionReceipt(ctx, params)
	var result CreatedReceipt
	switch {
	case err == nil:
		result.Receipt = receipt
	case errors.Is(err, pgx.ErrNoRows):
		receipt, err = existingReceipt(ctx, q, params)
		if err != nil {
			return CreatedReceipt{}, err
		}
		result.Receipt = receipt
		rows, err := q.ListContributionAllocationsByReceipt(ctx, receipt.ID)
		if err != nil {
			return CreatedReceipt{}, fmt.Errorf("list existing allocations: %w", err)
		}

		if err := verifyAllocationsMatch(allocations, rows); err != nil {
			return CreatedReceipt{}, fmt.Errorf(
				"verify existing receipt allocations: %w",
				err,
			)
		}

		return CreatedReceipt{
			Receipt:     receipt,
			Allocations: rows,
		}, nil

	default:
		return CreatedReceipt{}, fmt.Errorf("insert receipt: %w", err)
	}

	result.Allocations = make([]contributionsqlc.ContributionAllocation, 0, len(allocations))
	for _, allocation := range allocations {
		row, err := q.InsertContributionAllocation(ctx, contributionsqlc.InsertContributionAllocationParams{
			ReceiptID: result.Receipt.ID,
			Type:      allocation.Type,
			TargetID:  allocation.TargetID,
			Amount:    allocation.Amount,
		})
		switch {
		case err == nil:
			result.Allocations = append(result.Allocations, row)
		case errors.Is(err, pgx.ErrNoRows):
			return CreatedReceipt{}, fmt.Errorf(
				"%w: allocation %s already exists for newly created receipt %s",
				ErrInconsistentReceipt,
				allocationKey(
					allocation.Type,
					allocation.TargetID,
				),
				receipt.ID,
			)
		default:
			return CreatedReceipt{}, fmt.Errorf("insert allocation: %w", err)
		}
	}
	return result, nil
}

func (s *Service) GetReceipt(ctx context.Context, id pgtype.UUID) (contributionsqlc.ContributionReceipt, error) {
	receipt, err := s.store.GetContributionReceiptByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, ErrReceiptNotFound
		}
		return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt: %w", err)
	}
	return receipt, nil
}

// ProcessReceipt applies a persisted receipt to each owning ledger exactly once.
// Without this gate, webhooks and manual entries could mark receipts completed
// before Shares or Loans has accepted the money, which the final spec forbids.
func (s *Service) ProcessReceipt(ctx context.Context, receiptID, processedBy pgtype.UUID) (result CreatedReceipt, returnErr error) {
	// One atomic receipt performs multiple database round trips; recovery needs
	// enough time to reach Shares/Loans and commit, rather than restart every 5s.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	started := time.Now()
	defer func() {
		slog.Info("process contribution receipt", "receipt_id", receiptID.String(), "duration", time.Since(started), "status", result.Receipt.Status, "error", returnErr)
	}()
	var processErr error

	err := s.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		stageStarted := time.Now()
		q := contributionsqlc.New(tx)
		receipt, err := q.LockContributionReceiptByID(ctx, receiptID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				processErr = ErrReceiptNotFound
				return nil
			}
			return fmt.Errorf("lock receipt: %w", err)
		}
		result.Receipt = receipt

		allocations, err := q.LockContributionAllocationsByReceipt(ctx, receipt.ID)
		if err != nil {
			return fmt.Errorf("lock allocations: %w", err)
		}
		result.Allocations = allocations
		slog.Info("lock contribution receipt", "receipt_id", receiptID.String(), "duration", time.Since(stageStarted))

		switch receipt.Status {
		case contributionsqlc.ContributionReceiptStatusCompleted:
			return nil
		case contributionsqlc.ContributionReceiptStatusPending:
			receipt, err = q.UpdateContributionReceiptStatus(ctx, contributionsqlc.UpdateContributionReceiptStatusParams{
				ID:            receipt.ID,
				Status:        contributionsqlc.ContributionReceiptStatusProcessing,
				FailureReason: pgtype.Text{},
			})
			if err != nil {
				return fmt.Errorf("mark receipt processing: %w", err)
			}
			result.Receipt = receipt
		case contributionsqlc.ContributionReceiptStatusProcessing:
		default:
			processErr = ErrReceiptNotProcessable
			return nil
		}

		// Roll back all new postings before recording a business failure.
		if _, err := tx.Exec(ctx, "SAVEPOINT receipt_posting"); err != nil {
			return fmt.Errorf("begin receipt posting: %w", err)
		}
		if len(allocations) == 0 {
			processErr = ErrAllocationRequired
			return failReceipt(ctx, tx, q, receipt.ID, receiptFailureStatus(receipt), processErr, &result)
		}

		// Validate fees before posting into this transaction.
		// Excluding this receipt makes a retry safe after a partial posting.
		stageStarted = time.Now()
		fees, err := monthlyFees(ctx, q, receipt.MemberID, receipt.ContributionPeriod, receipt.ID)
		if err != nil {
			if receiptBusinessError(err) {
				processErr = err
				return failReceipt(ctx, tx, q, receipt.ID, receiptFailureStatus(receipt), processErr, &result)
			}
			return err
		}
		input := make([]AllocationInput, 0, len(allocations))
		for _, item := range allocations {
			input = append(input, AllocationInput{Type: item.Type, TargetID: item.TargetID, Amount: item.Amount})
		}
		expected, err := calculatedAllocations(input, fees)
		if err == nil {
			err = verifyAllocationsMatch(expected, allocations)
		}
		if err != nil {
			processErr = fmt.Errorf("%w: %v", ErrMembershipFeeMismatch, err)
			return failReceipt(ctx, tx, q, receipt.ID, receiptFailureStatus(receipt), processErr, &result)
		}
		if err := validateChargeAllocations(ctx, q, receipt.MemberID, receipt.BranchID, input, receipt.ID); err != nil {
			if !receiptBusinessError(err) {
				return err
			}
			processErr = err
			return failReceipt(ctx, tx, q, receipt.ID, receiptFailureStatus(receipt), processErr, &result)
		}
		// Keep target lock order consistent across receipts.
		slog.Info("validate contribution obligations", "receipt_id", receiptID.String(), "duration", time.Since(stageStarted))
		stageStarted = time.Now()
		loanQueries := loansqlc.New(tx)
		if _, err := loanQueries.LockMemberContributionLoans(ctx, loansqlc.LockMemberContributionLoansParams{MemberID: receipt.MemberID, BranchID: receipt.BranchID}); err != nil {
			return fmt.Errorf("lock receipt loan positions: %w", err)
		}
		loanPositions, err := loan.QuoteContributionLoans(ctx, loanQueries, receipt.MemberID, receipt.BranchID, receipt.ContributionPeriod)
		if err != nil {
			return err
		}
		var loanAllocationIDs []string
		slog.Info("quote contribution loans", "receipt_id", receiptID.String(), "duration", time.Since(stageStarted))
		canSnapshot := len(receipt.LoanSnapshot) == 0
		for _, allocation := range allocations {
			if allocation.Type == contributionsqlc.ContributionAllocationTypeLoanPrincipal {
				loanAllocationIDs = append(loanAllocationIDs, allocation.ID.String())
				// Older partial receipts have already reduced debt. Recovery must not
				// subtract that payment twice or invent a before-payment snapshot.
				if allocation.Status == contributionsqlc.ContributionAllocationStatusCompleted {
					canSnapshot = false
				}
			}
		}
		sort.Slice(allocations, func(i, j int) bool {
			if allocations[i].Type != allocations[j].Type {
				return allocations[i].Type < allocations[j].Type
			}
			return allocations[i].TargetID.String() < allocations[j].TargetID.String()
		})
		result.Allocations = make([]contributionsqlc.ContributionAllocation, 0, len(allocations))
		for _, allocation := range allocations {
			switch allocation.Status {
			case contributionsqlc.ContributionAllocationStatusCompleted:
				result.Allocations = append(result.Allocations, allocation)
				continue
			case contributionsqlc.ContributionAllocationStatusPending:
			default:
				processErr = ErrReceiptNotProcessable
				return failReceipt(ctx, tx, q, receipt.ID, receiptFailureStatus(receipt), processErr, &result)
			}

			started := time.Now()
			referenceID, externalReference, err := s.processAllocation(ctx, tx, receipt, allocation, receipt.ReceivedBy)
			slog.Info("post contribution allocation", "receipt_id", receipt.ID.String(), "allocation_id", allocation.ID.String(), "type", allocation.Type, "duration", time.Since(started), "error", err)
			if err != nil {
				processErr = fmt.Errorf("%w: %s", err, allocation.Type)
				if !receiptBusinessError(err) {
					return processErr
				}
				return failReceipt(ctx, tx, q, receipt.ID, receiptFailureStatus(receipt), processErr, &result)
			}

			allocation, err = q.UpdateContributionAllocationCompleted(ctx, contributionsqlc.UpdateContributionAllocationCompletedParams{
				ID:                       allocation.ID,
				AuthoritativeReferenceID: referenceID,
				ExternalReference:        externalReference,
			})
			if err != nil {
				return fmt.Errorf("mark allocation completed: %w", err)
			}
			result.Allocations = append(result.Allocations, allocation)
		}

		if canSnapshot {
			snapshot, err := loan.CompleteContributionLoanSnapshot(ctx, loanQueries, loanPositions, loanAllocationIDs)
			if err != nil {
				return err
			}
			receipt, err = q.SaveContributionLoanSnapshot(ctx, contributionsqlc.SaveContributionLoanSnapshotParams{ID: receipt.ID, LoanSnapshot: snapshot})
			if err != nil {
				return fmt.Errorf("save receipt loan snapshot: %w", err)
			}
		}
		receipt, err = q.UpdateContributionReceiptStatus(ctx, contributionsqlc.UpdateContributionReceiptStatusParams{
			ID:            receipt.ID,
			Status:        contributionsqlc.ContributionReceiptStatusCompleted,
			FailureReason: pgtype.Text{},
		})
		if err != nil {
			return fmt.Errorf("mark receipt completed: %w", err)
		}
		result.Receipt = receipt
		return nil
	})
	if err != nil {
		return CreatedReceipt{}, err
	}
	if processErr != nil {
		return result, processErr
	}
	return result, nil
}

// processAllocation keeps the cross-domain dispatch in one switch so adding a
// new allocation type cannot accidentally bypass the owning service rule.
// Removing it would push this switch into ProcessReceipt and make the receipt
// state transitions harder to audit.
func (s *Service) processAllocation(
	ctx context.Context,
	tx pgx.Tx,
	receipt contributionsqlc.ContributionReceipt,
	allocation contributionsqlc.ContributionAllocation,
	processedBy pgtype.UUID,
) (pgtype.UUID, pgtype.Text, error) {
	externalReference := receiptExternalReference(receipt)
	switch allocation.Type {
	case contributionsqlc.ContributionAllocationTypeCom,
		contributionsqlc.ContributionAllocationTypeLgom:
		return allocation.ID, externalReference, nil
	case contributionsqlc.ContributionAllocationTypeOtherCharge, contributionsqlc.ContributionAllocationTypePenalty:
		// ProcessReceipt has locked and validated the charge. Completion of this
		// allocation is the payment posting, atomic with its outstanding balance.
		return allocation.TargetID, externalReference, nil
	case contributionsqlc.ContributionAllocationTypeSharePurchase:
		if s.shareService == nil {
			return pgtype.UUID{}, pgtype.Text{}, ErrOwningServiceMissing
		}
		posted, err := s.shareService.PurchaseSharesInTx(
			ctx, sharesqlc.New(tx),
			allocation.TargetID,
			allocation.Amount,
			allocation.ID,
			processedBy,
			"contribution receipt "+receipt.ID.String(), receipt.MemberID, receipt.BranchID,
		)
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, err
		}
		return posted.ID, externalReference, nil
	case contributionsqlc.ContributionAllocationTypeLoanPrincipal:
		if s.loanService == nil {
			return pgtype.UUID{}, pgtype.Text{}, ErrOwningServiceMissing
		}
		posted, err := s.loanService.RecordRepaymentForPeriodInTx(ctx, loansqlc.New(tx), sharesqlc.New(tx), allocation.TargetID, allocation.Amount, allocation.ID.String(), processedBy, receipt.ContributionPeriod, receipt.MemberID, receipt.BranchID)
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, err
		}
		return posted.ID, externalReference, nil
	default:
		return pgtype.UUID{}, pgtype.Text{}, ErrAllocationNotSupported
	}
}

// failReceipt rolls back new ledger postings while retaining the receipt lock.
// Without the savepoint, a rejected allocation could commit earlier postings.
func failReceipt(
	ctx context.Context,
	tx pgx.Tx,
	q contributionsqlc.Querier,
	receiptID pgtype.UUID,
	status contributionsqlc.ContributionReceiptStatus,
	cause error,
	result *CreatedReceipt,
) error {
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT receipt_posting"); err != nil {
		return fmt.Errorf("rollback receipt postings: %w", err)
	}
	allocations, err := q.ListContributionAllocationsByReceipt(ctx, receiptID)
	if err != nil {
		return fmt.Errorf("read rolled back allocations: %w", err)
	}
	result.Allocations = allocations
	receipt, err := q.UpdateContributionReceiptStatus(ctx, contributionsqlc.UpdateContributionReceiptStatusParams{
		ID:            receiptID,
		Status:        status,
		FailureReason: text(cause.Error()),
	})
	if err != nil {
		return fmt.Errorf("mark receipt failed: %w", err)
	}
	result.Receipt = receipt
	return nil
}

// receiptBusinessError separates rejected allocations from temporary database failures.
// Unknown infrastructure errors must leave the persisted receipt available for retry.
func receiptBusinessError(err error) bool {
	return errors.Is(err, ErrInvalidPayment) || errors.Is(err, ErrAllocationRequired) || errors.Is(err, ErrReceiptNotProcessable) ||
		errors.Is(err, ErrMembershipFeeMismatch) || errors.Is(err, ErrInvalidCharge) ||
		errors.Is(err, ErrChargeOverpayment) || errors.Is(err, ErrInvalidAllocation) ||
		errors.Is(err, ErrDuplicateAllocation) || errors.Is(err, ErrAllocationNotSupported) ||
		errors.Is(err, share.ErrAccountNotFound) || errors.Is(err, share.ErrAccountNotActive) ||
		errors.Is(err, share.ErrDuplicateReference) || errors.Is(err, loan.ErrLoanNotFound) ||
		errors.Is(err, loan.ErrPaymentNotAllowed) || errors.Is(err, loan.ErrInvalidRepaymentPeriod) ||
		errors.Is(err, loan.ErrInvalidLoanAmount) || errors.Is(err, loan.ErrRepaymentAmountConflict) ||
		errors.Is(err, loan.ErrRepaymentScheduleMissing) || errors.Is(err, loan.ErrGatewayTransactionID)
}

// receiptFailureStatus keeps accepted cash visible to till reconciliation.
// Without it, a downstream allocation error would make physical money look
// like a payment that was never received.
func receiptFailureStatus(receipt contributionsqlc.ContributionReceipt) contributionsqlc.ContributionReceiptStatus {
	if receipt.SourceChannel == contributionsqlc.ContributionSourceChannelCash {
		return contributionsqlc.ContributionReceiptStatusManualReview
	}
	return contributionsqlc.ContributionReceiptStatusFailed
}

// receiptExternalReference preserves the source receipt reference on every
// completed allocation. Removing it would force reconciliation to join every
// allocation back to its cash or gateway receipt.
func receiptExternalReference(receipt contributionsqlc.ContributionReceipt) pgtype.Text {
	if receipt.ExternalTransactionID.Valid {
		return receipt.ExternalTransactionID
	}
	if receipt.InternalReceiptReference.Valid {
		return receipt.InternalReceiptReference
	}
	return receipt.CheckoutRequestID
}

func verifyAllocationsMatch(
	expected []AllocationInput,
	actual []contributionsqlc.ContributionAllocation,
) error {
	if len(expected) != len(actual) {
		return fmt.Errorf(
			"%w: expected %d allocations, found %d",
			ErrInconsistentReceipt,
			len(expected),
			len(actual),
		)
	}

	actualAmounts := make(map[string]*big.Int, len(actual))

	for _, allocation := range actual {
		key := allocationKey(
			allocation.Type,
			allocation.TargetID,
		)

		if _, exists := actualAmounts[key]; exists {
			return fmt.Errorf(
				"%w: duplicate stored allocation %q",
				ErrInconsistentReceipt,
				key,
			)
		}

		actualAmounts[key] = numericToScale(
			allocation.Amount,
			-4,
		)
	}

	seenExpected := make(map[string]struct{}, len(expected))

	for _, allocation := range expected {
		key := allocationKey(
			allocation.Type,
			allocation.TargetID,
		)

		if _, exists := seenExpected[key]; exists {
			return fmt.Errorf(
				"%w: duplicate expected allocation %q",
				ErrInconsistentReceipt,
				key,
			)
		}

		seenExpected[key] = struct{}{}

		actualAmount, exists := actualAmounts[key]
		if !exists {
			return fmt.Errorf(
				"%w: expected allocation %q is missing",
				ErrInconsistentReceipt,
				key,
			)
		}

		expectedAmount := numericToScale(
			allocation.Amount,
			-4,
		)

		if expectedAmount.Cmp(actualAmount) != 0 {
			return fmt.Errorf(
				"%w: amount mismatch for allocation %q",
				ErrInconsistentReceipt,
				key,
			)
		}
	}

	return nil
}

func existingReceipt(
	ctx context.Context,
	q contributionsqlc.Querier,
	params contributionsqlc.InsertContributionReceiptParams,
) (contributionsqlc.ContributionReceipt, error) {
	if params.IdempotencyKey.Valid {
		receipt, err := q.GetContributionReceiptByIdempotencyKey(ctx, params.IdempotencyKey)
		if err == nil {
			return receipt, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt by idempotency key: %w", err)
		}
	}
	if params.ExternalTransactionID.Valid {
		receipt, err := q.GetContributionReceiptByExternalTransactionID(ctx, params.ExternalTransactionID)
		if err == nil {
			return receipt, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt by external transaction id: %w", err)
		}
	}
	if params.CheckoutRequestID.Valid {
		receipt, err := q.GetContributionReceiptByCheckoutRequestID(ctx, params.CheckoutRequestID)
		if err == nil {
			return receipt, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt by checkout request id: %w", err)
		}
	}
	return contributionsqlc.ContributionReceipt{}, ErrReceiptNotFound
}

func validateReceipt(params contributionsqlc.InsertContributionReceiptParams, allocations []AllocationInput) error {
	if !positive(params.ReceivedAmount) {
		return ErrInvalidReceiptAmount
	}
	switch params.SourceChannel {
	case contributionsqlc.ContributionSourceChannelDarajaStk,
		contributionsqlc.ContributionSourceChannelCash,
		contributionsqlc.ContributionSourceChannelManual:
	default:
		return ErrInvalidPayment
	}
	if !json.Valid(params.AllocationPlan) {
		return ErrInvalidAllocationPlan
	}
	if len(allocations) == 0 {
		return ErrAllocationRequired
	}
	hasReference := params.ExternalTransactionID.Valid && strings.TrimSpace(params.ExternalTransactionID.String) != ""
	hasReference = hasReference || params.CheckoutRequestID.Valid && strings.TrimSpace(params.CheckoutRequestID.String) != ""
	hasReference = hasReference || params.InternalReceiptReference.Valid && strings.TrimSpace(params.InternalReceiptReference.String) != ""
	if !hasReference {
		return ErrReceiptReferenceRequired
	}

	total := new(big.Int)
	seen := make(map[string]struct{}, len(allocations))
	for _, allocation := range allocations {
		if !positive(allocation.Amount) {
			return ErrInvalidAllocation
		}
		key := string(allocation.Type)
		if allocation.TargetID.Valid {
			key += ":" + allocation.TargetID.String()
		}
		if _, ok := seen[key]; ok {
			return ErrDuplicateAllocation
		}
		seen[key] = struct{}{}

		switch allocation.Type {
		case contributionsqlc.ContributionAllocationTypeSharePurchase,
			contributionsqlc.ContributionAllocationTypeLoanPrincipal,
			contributionsqlc.ContributionAllocationTypeLoanInterest,
			contributionsqlc.ContributionAllocationTypePenalty,
			contributionsqlc.ContributionAllocationTypeOtherCharge,
			contributionsqlc.ContributionAllocationTypeOverpaymentCredit:
			// Owning-domain allocations must name the exact ledger target up
			// front. Inferring it later from amount, member or phone number is
			// how shares get posted as loan repayments or to the wrong member.
			if !allocation.TargetID.Valid {
				return ErrInvalidAllocation
			}
		}
		total.Add(total, numericToScale(allocation.Amount, -4))
	}
	if total.Cmp(numericToScale(params.ReceivedAmount, -4)) != 0 {
		return ErrAllocationTotalMismatch
	}
	return nil
}
func allocationKey(
	allocationType contributionsqlc.ContributionAllocationType,
	targetID pgtype.UUID,
) string {
	key := string(allocationType)

	if targetID.Valid {
		return key + ":" + targetID.String()
	}

	return key + ":<none>"
}

func positive(n pgtype.Numeric) bool {
	return n.Valid && n.Int != nil && n.Int.Sign() > 0
}

func numericToScale(n pgtype.Numeric, scale int32) *big.Int {
	if !n.Valid || n.Int == nil {
		return new(big.Int)
	}

	out := new(big.Int).Set(n.Int)
	diff := n.Exp - scale
	switch {
	case diff > 0:
		out.Mul(out, pow10(diff))
	case diff < 0:
		out.Quo(out, pow10(-diff))
	}
	return out
}

func pow10(exp int32) *big.Int {
	result := big.NewInt(1)
	ten := big.NewInt(10)
	for i := int32(0); i < exp; i++ {
		result.Mul(result, ten)
	}
	return result
}

// text is the smallest local adapter from Go strings to sqlc's pgtype.Text.
// Removing it would repeat the Valid flag construction at each status update.
func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
