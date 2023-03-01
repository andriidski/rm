package analysis

import (
	"context"

	"github.com/holiman/uint256"
	"github.com/ralexstokes/relay-monitor/pkg/crypto"
	"github.com/ralexstokes/relay-monitor/pkg/types"
)

func (a *Analyzer) validatePublicKey(ctx context.Context, bidCtx *types.BidContext, bid *types.Bid) (*types.InvalidBid, error) {
	if bidCtx.RelayPublicKey != bid.Message.Pubkey {
		return &types.InvalidBid{
			Category: types.InvalidBidPublicKeyCategory,
			Reason:   types.AnalysisReasonIncorrectPublicKey,
		}, nil
	}

	return nil, nil
}

func (a *Analyzer) validateSignature(ctx context.Context, bidCtx *types.BidContext, bid *types.Bid) (*types.InvalidBid, error) {
	validSignature, err := crypto.VerifySignature(bid.Message, a.consensusClient.SignatureDomainForBuilder(), bid.Message.Pubkey[:], bid.Signature[:])
	if err != nil {
		return nil, err
	}

	if !validSignature {
		return &types.InvalidBid{
			Category: types.InvalidBidSignatureCategory,
			Reason:   types.AnalysisReasonInvalidSignature,
		}, nil
	}

	return nil, nil
}

func (a *Analyzer) validateHeader(ctx context.Context, bidCtx *types.BidContext, bid *types.Bid) (*types.InvalidBid, error) {
	header := bid.Message.Header

	// Validate the hash itself.
	if bidCtx.ParentHash != header.ParentHash {
		return &types.InvalidBid{
			Category: types.InvalidBidConsensusCategory,
			Reason:   types.AnalysisReasonInvalidParentHash,
		}, nil
	}

	// Verify the RANDAO value.
	expectedRandomness, err := a.consensusClient.GetRandomnessForProposal(bidCtx.Slot)
	if err != nil {
		return nil, err
	}
	if expectedRandomness != header.Random {
		return &types.InvalidBid{
			Category: types.InvalidBidConsensusCategory,
			Reason:   types.AnalysisReasonInvalidRandomValue,
		}, nil
	}

	// Verify the block number.
	expectedBlockNumber, err := a.consensusClient.GetBlockNumberForProposal(bidCtx.Slot)
	if err != nil {
		return nil, err
	}
	if expectedBlockNumber != header.BlockNumber {
		return &types.InvalidBid{
			Category: types.InvalidBidConsensusCategory,
			Reason:   types.AnalysisReasonInvalidBlockNumber,
		}, nil
	}

	// Verify that the bid gas used is less than the gas limit.
	if header.GasUsed > header.GasLimit {
		return &types.InvalidBid{
			Category: types.InvalidBidConsensusCategory,
			Reason:   types.AnalysisReasonInvalidGasUsed,
		}, nil
	}

	// Verify the timestamp.
	expectedTimestamp := a.clock.SlotInSeconds(bidCtx.Slot)
	if expectedTimestamp != int64(header.Timestamp) {
		return &types.InvalidBid{
			Category: types.InvalidBidConsensusCategory,
			Reason:   types.AnalysisReasonInvalidTimestamp,
		}, nil
	}

	// Verify the base fee.
	expectedBaseFee, err := a.consensusClient.GetBaseFeeForProposal(bidCtx.Slot)
	if err != nil {
		return nil, err
	}
	baseFee := uint256.NewInt(0)
	baseFee.SetBytes(reverse(header.BaseFeePerGas[:]))
	if !expectedBaseFee.Eq(baseFee) {
		return &types.InvalidBid{
			Category: types.InvalidBidConsensusCategory,
			Reason:   types.AnalysisReasonInvalidBaseFee,
		}, nil
	}

	return nil, nil
}

func (a *Analyzer) validateGasLimit(ctx context.Context, gasLimit uint64, gasLimitPreference uint64, blockNumber uint64) (bool, error) {
	if gasLimit == gasLimitPreference {
		return true, nil
	}

	parentGasLimit, err := a.consensusClient.GetParentGasLimit(ctx, blockNumber)
	if err != nil {
		return false, err
	}

	var expectedBound uint64
	if gasLimitPreference > gasLimit {
		expectedBound = parentGasLimit + (parentGasLimit / GasLimitBoundDivisor)
	} else {
		expectedBound = parentGasLimit - (parentGasLimit / GasLimitBoundDivisor)
	}

	return gasLimit == expectedBound, nil
}

func (a *Analyzer) validateValidatorPrefence(ctx context.Context, bidCtx *types.BidContext, bid *types.Bid) (*types.InvalidBid, error) {
	logger := a.logger.Sugar()

	header := bid.Message.Header

	registration, err := a.store.GetLatestValidatorRegistration(ctx, &bidCtx.ProposerPublicKey)
	if err != nil {
		return nil, err
	}

	// Only validate if there is a registration.
	if registration != nil {
		// Validate the fee recipient.
		if registration.Message.FeeRecipient != header.FeeRecipient {
			return &types.InvalidBid{
				Reason:   types.AnalysisReasonIgnoredValidatorPreferenceFeeRecipient,
				Category: types.InvalidBidIgnoredPreferencesCategory,
			}, nil
		}

		// Validate the gas limit preference.
		gasLimitPreference := registration.Message.GasLimit

		// NOTE: need transaction set for possibility of payment transaction
		// so we defer analysis of fee recipient until we have the full payload

		valid, err := a.validateGasLimit(ctx, header.GasLimit, gasLimitPreference, header.BlockNumber)
		if err != nil {
			return nil, err
		}
		if !valid {
			return &types.InvalidBid{
				Reason:   types.AnalysisReasonIgnoredValidatorPreferenceGasLimit,
				Category: types.InvalidBidIgnoredPreferencesCategory,
			}, nil
		}
	} else {
		logger.Infow("validator not registered", "proposer", bidCtx.ProposerPublicKey.String())
	}

	return nil, nil
}

func (a *Analyzer) validateBid(ctx context.Context, bidCtx *types.BidContext, bid *types.Bid) (*types.InvalidBid, error) {
	if bid == nil {
		return nil, nil
	}

	// Validate that the public key is correct.
	invalidBid, err := a.validatePublicKey(ctx, bidCtx, bid)
	if err != nil {
		return nil, err
	}
	if invalidBid != nil {
		return invalidBid, nil
	}

	// Validate that the signature is correct.
	invalidBid, err = a.validateSignature(ctx, bidCtx, bid)
	if err != nil {
		return nil, err
	}
	if invalidBid != nil {
		return invalidBid, nil
	}

	// Validate the header.
	invalidBid, err = a.validateHeader(ctx, bidCtx, bid)
	if err != nil {
		return nil, err
	}
	if invalidBid != nil {
		return invalidBid, nil
	}

	// Validate validator preferences.
	invalidBid, err = a.validateValidatorPrefence(ctx, bidCtx, bid)
	if err != nil {
		return nil, err
	}
	if invalidBid != nil {
		return invalidBid, nil
	}

	return nil, nil
}
