package analysis

import (
	"context"

	"github.com/ralexstokes/relay-monitor/pkg/builder"
	"github.com/ralexstokes/relay-monitor/pkg/consensus"
	"github.com/ralexstokes/relay-monitor/pkg/crypto"
	"github.com/ralexstokes/relay-monitor/pkg/data"
	"github.com/ralexstokes/relay-monitor/pkg/store"
	"github.com/ralexstokes/relay-monitor/pkg/types"
	"go.uber.org/zap"
)

const (
	GasLimitBoundDivisor = 1024
)

type Analyzer struct {
	logger *zap.Logger

	events <-chan data.Event

	store           store.Storer
	consensusClient *consensus.Client
	clock           *consensus.Clock
}

func NewAnalyzer(logger *zap.Logger, relays []*builder.Client, events <-chan data.Event, store store.Storer, consensusClient *consensus.Client, clock *consensus.Clock) *Analyzer {
	return &Analyzer{
		logger:          logger,
		events:          events,
		store:           store,
		consensusClient: consensusClient,
		clock:           clock,
	}
}

// borrowed from `flashbots/go-boost-utils`
func reverse(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	for i := len(dst)/2 - 1; i >= 0; i-- {
		opp := len(dst) - 1 - i
		dst[i], dst[opp] = dst[opp], dst[i]
	}
	return dst
}

/*

TODO see what steps (if any) should be added for complete analysis. Notes on some things to verify

Outline for how to analyze a bid
When bid comes in (slot updated)
* try to fetch from the relay (since we are checking what the relay is claiming to be the winning bid)
* if didn’t receive, schedule a re-try
* if received,
* verify all fields present
* verify each field
* verify signature
* save verification into DB
*/

func (analyzer *Analyzer) processBid(ctx context.Context, event *data.BidEvent) {
	logger := analyzer.logger.Sugar()

	bidCtx := event.Context
	bid := event.Bid

	// Store the bid.
	// TODO consider storing earlier and only analyze the bid here / read from DB instead of
	// getting the event from a channel.
	err := analyzer.store.PutBid(ctx, bidCtx, bid)
	if err != nil {
		logger.Warnf("could not store bid: %+v", event)
		return
	}

	// Validate the bid and return any errors.
	invalidBid, err := analyzer.validateBid(ctx, bidCtx, bid)
	if err != nil {
		logger.Warnf("could not validate bid with error %+v: %+v, %+v", err, bidCtx, bid)
		return
	}

	// Store the analysis result.
	err = analyzer.store.PutBidAnalysis(ctx, bidCtx, invalidBid)
	if err != nil {
		logger.Warnf("could not store analysis result with error %+v: %+v, %+v", err, bidCtx, bid)
		return
	}

	// TODO scope faults by coordinate
	if invalidBid != nil {
		logger.Debugf("invalid bid: %+v, %+v", invalidBid, event)
	} else {
		logger.Debugf("processed valid bid: %+v, %+v", bidCtx, bid)
	}
}

// Process incoming validator registrations
// This data has already been validated by the sender of the event
func (a *Analyzer) processValidatorRegistration(ctx context.Context, event data.ValidatorRegistrationEvent) {
	logger := a.logger.Sugar()

	registrations := event.Registrations
	logger.Debugf("received %d validator registrations", len(registrations))
	for _, registration := range registrations {
		err := a.store.PutValidatorRegistration(ctx, &registration)
		if err != nil {
			logger.Warnf("could not store validator registration: %+v", registration)
			return
		}
	}
}

func (a *Analyzer) processAuctionTranscript(ctx context.Context, event data.AuctionTranscriptEvent) {
	logger := a.logger.Sugar()

	logger.Debugf("received transcript: %+v", event.Transcript)

	transcript := event.Transcript

	bid := transcript.Bid.Message
	signedBlindedBeaconBlock := &transcript.Acceptance
	blindedBeaconBlock := signedBlindedBeaconBlock.Message

	// Verify signature first, to avoid doing unnecessary work in the event this is a "bad" transcript
	proposerPublicKey, err := a.consensusClient.GetPublicKeyForIndex(ctx, blindedBeaconBlock.ProposerIndex)
	if err != nil {
		logger.Warnw("could not find public key for validator index", "error", err)
		return
	}

	domain := a.consensusClient.SignatureDomain(blindedBeaconBlock.Slot)
	valid, err := crypto.VerifySignature(signedBlindedBeaconBlock.Message, domain, proposerPublicKey[:], signedBlindedBeaconBlock.Signature[:])
	if err != nil {
		logger.Warnw("error verifying signature from proposer; could not determine authenticity of transcript", "error", err, "bid", bid, "acceptance", signedBlindedBeaconBlock)
		return
	}
	if !valid {
		logger.Warnw("signature from proposer was invalid; could not determine authenticity of transcript", "error", err, "bid", bid, "acceptance", signedBlindedBeaconBlock)
		return
	}

	bidCtx := &types.BidContext{
		Slot:              blindedBeaconBlock.Slot,
		ParentHash:        bid.Header.ParentHash,
		ProposerPublicKey: *proposerPublicKey,
		RelayPublicKey:    bid.Pubkey,
	}
	existingBid, err := a.store.GetBid(ctx, bidCtx)
	if err != nil {
		logger.Warnw("could not find existing bid, will continue full analysis", "context", bidCtx)

		// TODO: process bid as well as rest of transcript
	}

	if existingBid != nil && *existingBid != transcript.Bid {
		logger.Warnw("provided bid from transcript did not match existing bid, will continue full analysis", "context", bidCtx)

		// TODO: process bid as well as rest of transcript
	}

	// TODO also store bid if missing?
	err = a.store.PutAcceptance(ctx, bidCtx, signedBlindedBeaconBlock)
	if err != nil {
		logger.Warnf("could not store bid acceptance data: %+v", event)
		return
	}

	// verify later w/ full payload:
	// (claimed) Value, including fee recipient
	// expectedFeeRecipient := registration.Message.FeeRecipient
	// if expectedFeeRecipient != header.FeeRecipient {
	// 	return &types.InvalidBid{
	// 		Reason: "invalid fee recipient",
	// 		Type:   types.InvalidBidIgnoredPreferencesType,
	// 		Context: map[string]interface{}{
	// 			"expected fee recipient":  expectedFeeRecipient,
	// 			"fee recipient in header": header.FeeRecipient,
	// 		},
	// 	}, nil
	// }

	// BlockHash
	// StateRoot
	// ReceiptsRoot
	// LogsBloom
	// TransactionsRoot

	// TODO save analysis results

}

func (a *Analyzer) Run(ctx context.Context) error {
	logger := a.logger.Sugar()

	for {
		select {
		case event := <-a.events:
			switch event := event.Payload.(type) {
			case *data.BidEvent:
				a.processBid(ctx, event)
			case data.ValidatorRegistrationEvent:
				a.processValidatorRegistration(ctx, event)
			case data.AuctionTranscriptEvent:
				a.processAuctionTranscript(ctx, event)
			default:
				logger.Warnf("unknown event type %T for event %+v!", event, event)
			}
		case <-ctx.Done():
			return nil
		}
	}
}
