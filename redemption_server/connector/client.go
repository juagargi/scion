package connector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"time"

	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
	"github.com/scionproto/scion/pkg/hummingbird/id_stores"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	shummingbird "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/redemption_server/config"
	"github.com/scionproto/scion/redemption_server/db"
	"github.com/scionproto/scion/redemption_server/storage"
)

type Connector struct {
	redemptionClient hummingbirdconnect.RedemptionServiceClient
	secretValue      cipher.Block
	id_store         *id_stores.UsedIDStore
	buffer           []byte
	store            *storage.RedemptionStorage
}

func NewConnector(ctx context.Context, masterKey []byte, cfg *config.MarketplaceConfig,
	redemptionClient hummingbirdconnect.RedemptionServiceClient, store *storage.RedemptionStorage,
) (*Connector, error) {
	svc := shummingbird.DeriveSecretValueWithSalt(masterKey, cfg.KeySalt)
	secretValue, err := aes.NewCipher(svc)
	if err != nil {
		return nil, err
	}
	reservations, err := store.FetchReservations(ctx, db.ReservationQuery{})
	if err != nil {
		return nil, err
	}
	id_store := &id_stores.UsedIDStore{}
	id_store.Init(cfg.ResIdLimitLow, cfg.ResIdLimitHigh, reservations)
	return &Connector{
		redemptionClient: redemptionClient,
		id_store:         id_store,
		buffer:           make([]byte, 16),
		secretValue:      secretValue,
		store:            store,
	}, nil
}

func (c *Connector) StartRedemption(ctx context.Context) error {
	stream := c.redemptionClient.RedeemASAsset(ctx)
	err := stream.Send(nil) // required to open the connection
	if err != nil {
		return err
	}
	for {
		msg, err := stream.Receive()
		if err != nil {
			log.Debug("Receive error", "err", err)
			return err
		}
		log.Debug("received request", "id", msg.RequestId)
		reply := c.handleRequest(ctx, msg)
		if err := stream.Send(reply); err != nil {
			log.Debug("Send error", "err", err)
		}
		log.Debug("replied request", "id", reply.RequestId)
	}
}

func (c *Connector) validateRequest(msg *hummingbird.RedeemAssetFromASRequest) error {
	if msg.StopsAt.Seconds <= msg.StartsAt.Seconds {
		return serrors.New("invalid reservation duration")
	}
	return nil
}

func (c *Connector) handleRequest(ctx context.Context, msg *hummingbird.RedeemAssetFromASRequest,
) *hummingbird.RedeemAssetFromASResponse {
	returnErr := func(err error) *hummingbird.RedeemAssetFromASResponse {
		return &hummingbird.RedeemAssetFromASResponse{
			RequestId: msg.RequestId,
			Result: &hummingbird.RedeemAssetFromASResponse_Error{
				Error: err.Error(),
			},
		}
	}
	if err := c.validateRequest(msg); err != nil {
		return returnErr(err)
	}
	resId, err := c.id_store.Next(time.Now().Unix(), msg.StartsAt.Seconds, msg.StopsAt.Seconds)
	if err != nil {
		return returnErr(err)
	}
	unixStart := uint32(msg.StartsAt.Seconds)
	unixEnd := uint32(msg.StopsAt.Seconds)
	duration := uint16(unixEnd - unixStart)
	bw_rounded, encoded_bw := bwencoding.EncodeBandwidth(msg.Bandwidth)
	authKey := shummingbird.DeriveAuthKey(c.secretValue, resId, encoded_bw,
		uint16(msg.IngressId), uint16(msg.EgressId), unixStart, duration, c.buffer)
	_, err = c.store.InsertReservation(ctx, &db.DBReservation{
		ReservationID: resId,
		Ingress:       uint16(msg.IngressId),
		Egress:        uint16(msg.EgressId),
		StartsAt:      msg.StartsAt.AsTime(),
		StopsAt:       msg.StopsAt.AsTime(),
	})
	if err != nil {
		return returnErr(err)
	}
	return &hummingbird.RedeemAssetFromASResponse{
		Result: &hummingbird.RedeemAssetFromASResponse_ResInfo{
			ResInfo: &hummingbird.ReservationInfo{
				ReservationId:       resId,
				BandwithRounded:     bw_rounded,
				BwDataplaneEncoding: uint32(encoded_bw),
				AuthenticationKey:   authKey,
			},
		},
		RequestId: msg.RequestId,
	}
}
