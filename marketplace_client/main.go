// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/daemon"
	"github.com/scionproto/scion/pkg/daemon/types"
	hbird "github.com/scionproto/scion/pkg/hummingbird"
	marketclient "github.com/scionproto/scion/pkg/hummingbird/marketplace"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	"github.com/scionproto/scion/pkg/snet"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type jwtType int

const (
	User jwtType = iota
	Publisher
	RedemptionService
)

func printOptions(t jwtType) {
	fmt.Println("-> info")
	switch t {
	case User:
		fmt.Println("-> search")
		fmt.Println("-> buy")
		fmt.Println("-> split")
		fmt.Println("-> combine")
		fmt.Println("-> redeem")
		fmt.Println("-> reservation")
	case Publisher:
		fmt.Println("-> search")
		fmt.Println("-> publish")
		fmt.Println("-> update")
		fmt.Println("-> statistics")
		fmt.Println("-> password")
	case RedemptionService:
		fmt.Println("-> delegate")
	}
	fmt.Println("-> reset")
	fmt.Println("-> exit")
}

type HummingbirdNotes struct {
	Hummingbird []HummingbirdNoteEntry `json:"hummingbird,omitempty"`
}

type HummingbirdNoteEntry struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Api      string `json:"api"`
	Website  string `json:"client_registration_website"`
}

func discoverMarketplaces(ctx context.Context, reader *bufio.Reader) (*HummingbirdNoteEntry, error) {
	fmt.Println("Discovery will return a list of marketplaces that offer hummingbird reservations for a given AS.")
	targetIA := readString(reader, "Reservation ISD-AS: ")
	ia, err := addr.ParseIA(targetIA)
	if err != nil {
		return nil, err
	}

	var paths []snet.Path

	connector, err := daemon.NewAutoConnector(ctx, daemon.WithDaemon(sciond))
	if err != nil {
		return nil, err
	}
	localIA, err := connector.LocalIA(ctx)
	if err != nil {
		return nil, err
	}
	paths, err = connector.Paths(ctx, ia, localIA, types.PathReqFlags{})
	if err != nil {
		return nil, err
	}

	marketplacesSet := map[HummingbirdNoteEntry]int{}
	for _, path := range paths {
		for _, note := range path.Metadata().Notes {
			if note == "" {
				continue
			}
			hummingbirdNotes := &HummingbirdNotes{}
			err = json.Unmarshal([]byte(note), hummingbirdNotes)
			if err != nil {
				fmt.Println(err, note)
				continue
			}
			for _, entry := range hummingbirdNotes.Hummingbird {
				marketplacesSet[entry]++
			}
		}
	}
	if len(marketplacesSet) == 0 {
		return nil, serrors.New("No marketplaces found")
	}
	marketplaces := make([]HummingbirdNoteEntry, 0, len(marketplacesSet))
	for marketplace := range marketplacesSet {
		marketplaces = append(marketplaces, marketplace)
	}
	sort.Slice(marketplaces, func(i, j int) bool {
		return marketplacesSet[marketplaces[i]] < marketplacesSet[marketplaces[j]]
	})
	fmt.Println("Found marketplaces:")

	for i, marketplace := range marketplaces {
		fmt.Printf("%d: %s with website %s using protocol %s\n", i, marketplace.Name, marketplace.Website, marketplace.Protocol)
	}
	index := readUint64(reader, "Select marketplace: ")
	if index >= uint64(len(marketplaces)) {
		return nil, serrors.New("index out of range")
	}
	return &marketplaces[index], nil
}

func tokenType(tokenStr string) (string, jwtType, error) {
	parseScopes := func(scopeStr string) map[string]bool {
		scopes := make(map[string]bool)
		for s := range strings.SplitSeq(scopeStr, ",") {
			if s != "" {
				scopes[s] = true
			}
		}
		return scopes
	}
	parser := jwt.Parser{}
	publisherClaims := jwt.MapClaims{}
	token, _, err := parser.ParseUnverified(tokenStr, publisherClaims)
	if err != nil {
		return "", 0, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", 0, serrors.New("invalid claims")
	}

	user, ok := claims["sub"].(string)
	if !ok || user == "" {
		return "", 0, serrors.New("missing subject")
	}
	scopeStr, ok := claims["scope"].(string)
	if !ok || scopeStr == "" {
		return "", 0, serrors.New("missing scope")
	}
	scopes := parseScopes(scopeStr)
	if scopes[registration.ScopeUser] {
		return user, User, nil
	} else if scopes[registration.ScopeAssetPublisher] {
		return user, Publisher, nil
	} else if scopes[registration.ScopeRedemptionService] {
		return user, RedemptionService, nil
	} else {
		return "", 0, serrors.New("unsupported JWT")
	}
}

func userInteraction() {
	ctx := context.Background()
	reader := bufio.NewReader(os.Stdin)
	url := readString(reader, "marketplace_api (leave empty to start discovery): ")
	var err error
	if url == "" {
		if sciond == "" {
			fmt.Println("marketplace discovery is only enabled when providing a SCION daemon address")
			return
		}

		marketplace, err := discoverMarketplaces(ctx, reader)
		if err != nil {
			fmt.Println(err)
			return
		}
		url = marketplace.Api
	}
	token := ""
	if !as_registration {
		token = readString(reader, "jwt_token: ")
	}
	var localIA addr.IA
	if as_registration {
		localIAString := readString(reader, "local IA: ")
		localIA, err = addr.ParseIA(localIAString)
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	clientOptions := marketclient.ClientOptions{Insecure: insecure}
	if marketclient.IsSCIONURL(url) {
		if sciond == "" {
			fmt.Println("connecting over SCION requires a SCION daemon address")
			return
		}
		connector, err := daemon.NewAutoConnector(ctx, daemon.WithDaemon(sciond))
		if err != nil {
			fmt.Println(err)
			return
		}
		topo, err := daemon.LoadTopology(ctx, connector)
		if err != nil {
			fmt.Println(err)
			return
		}
		clientOptions.Topology = topo
		clientOptions.Querier = daemon.Querier{Connector: connector, IA: topo.LocalIA}
	}
	clients, err := marketclient.NewClientSet(ctx, url, token, clientOptions)
	if err != nil {
		fmt.Println(err)
		return
	}
	marketplaceClient := clients.Marketplace
	redemptionClient := clients.Redemption
	accountClient := clients.Account
	if clients.SCION {
		fmt.Println("use SCION connection")
	} else {
		fmt.Println("use TCP connection")
	}
	if as_registration {
		trcDir := readString(reader, "trc directory: ")
		certDir := readString(reader, "certificate directory: ")
		keyRingDir := readString(reader, "keyring directory: ")
		regClient := registration.NewClient(accountClient, clients.Authority)
		publisherToken, redemptionToken, err := regClient.RegisterWithNewSigner(ctx, localIA, trcDir, certDir, keyRingDir)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Printf("Publisher Token: %s\nRedemptionService Token: %s\n", publisherToken, redemptionToken)
		return
	}
	username, t, err := tokenType(token)
	if err == nil {
		fmt.Printf("Logged in as %s\n", username)
	}

	for {
		printOptions(t)
		option, err := reader.ReadString('\n')
		option = strings.TrimSpace(option)
		if err != nil {
			fmt.Println(err)
			continue
		}
		switch {
		case option == "info":
			handleInfo(ctx, marketplaceClient)
		case option == "search":
			handleSearch(ctx, reader, marketplaceClient)
		case option == "buy":
			handleBuy(ctx, reader, marketplaceClient)
		case option == "split":
			handleSplit(ctx, reader, marketplaceClient)
		case option == "combine":
			handleCombine(ctx, reader, marketplaceClient)
		case option == "redeem":
			handleRedeem(ctx, reader, marketplaceClient)
		case option == "reservation":
			handleReservation(ctx, reader, marketplaceClient)
		case option == "publish":
			handlePublish(ctx, reader, marketplaceClient)
		case option == "statistics":
			handleStatistics(ctx, reader, marketplaceClient)
		case option == "delegate":
			handleDelegate(ctx, reader, redemptionClient)
		case option == "update":
			handleUpdate(ctx, reader, marketplaceClient)
		case option == "password":
			handlePassword(ctx, reader, accountClient)
		case option == "reset":
			if success := handleResetJwt(ctx, reader, accountClient, t); success {
				return
			}
		case option == "exit":
			return
		}
		fmt.Println("----------")
	}
}

func handlePassword(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.AccountServiceClient) {
	fmt.Println("Handle set authentication token query")
	password := readString(reader, "password: ")
	_, err := c.SetPassword(ctx, &connect.Request[hummingbird.SetPasswordRequest]{
		Msg: &hummingbird.SetPasswordRequest{
			Password: password,
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Authentication Token updated!")
}

func handleResetJwt(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.AccountServiceClient, t jwtType) bool {
	fmt.Println("Handle jwt reset query")
	if t == Publisher || t == RedemptionService {
		fmt.Println("Warning! Reseting the Token will also terminate the connection between the marketplace and the redemption service!")
	}
	if !readConfirm(reader, nil) {
		return false
	}
	_, err := c.ResetJWT(ctx, &connect.Request[hummingbird.JWTResetRequest]{})
	if err != nil {
		fmt.Println(err)
		return false
	}
	fmt.Println("token reseted")
	return true
}

func handleDelegate(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.RedemptionServiceClient) {
	fmt.Println("Handle redemption delegation query")
	expTime := readTime(reader, "Redemption until (2026-06-23T13:25:36Z): ")
	idLowerBound := uint32(readUint64(reader, "Reservation ID lower bound: "))
	idUpperBound := uint32(readUint64(reader, "Reservation ID upper bound: "))
	hexStr := readString(reader, "Key in hexadecimal (a1b2c3): ")
	key, err := hex.DecodeString(hexStr)
	if err != nil {
		fmt.Println(err)
		return
	}
	if len(key) != 16 {
		fmt.Println("key has invalid length", "expected", 16, "got", len(key))
		return
	}
	pathToEncodings := readString(reader, "path to encodings file(csv): ")
	f, err := os.Open(pathToEncodings)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()

	r := csv.NewReader(f)

	record, err := r.Read()
	if err != nil {
		fmt.Println(err)
	}

	encodings := make([]uint32, len(record))

	for i, s := range record {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			fmt.Println(err)
			return
		}
		encodings[i] = uint32(v)
	}
	if !readConfirm(reader, nil) {
		return
	}

	resp, err := c.DelegateRedemption(ctx, &connect.Request[hummingbird.DelegateRedemptionRequest]{
		Msg: &hummingbird.DelegateRedemptionRequest{
			ExpirationTime:          timestamppb.New(expTime),
			ReservationIdLowerBound: idLowerBound,
			ReservationIdUpperBound: idUpperBound,
			Key:                     key,
			EncodingPoints:          encodings,
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Delegated until:", resp.Msg.ExpirationTime.AsTime())
}
func handlePublish(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Handle publish asset query.")
	asset := &hummingbird.PublisherAsset{
		IfIdIngress:     readOptionalUint32(reader, "ingress: "),
		IfIdEgress:      readOptionalUint32(reader, "egress: "),
		Bandwidth:       uint32(readUint64(reader, "bandwidth: ")),
		BandwidthMin:    uint32(readUint64(reader, "minimum bandwidth: ")),
		BandwidthMax:    uint32(readUint64(reader, "maximum bandwidth: ")),
		StartsAt:        timestamppb.New(readTime(reader, "Starts at (2026-06-23T13:25:36Z): ")),
		StopsAt:         timestamppb.New(readTime(reader, "Stops at (2026-06-23T13:25:36Z): ")),
		Price:           uint32(readUint64(reader, "price per kbit per second: ")),
		TimeMinDuration: uint32(readUint64(reader, "minimum time duration: ")),
		TimeMaxDuration: uint32(readUint64(reader, "maximum time duration: ")),
		TimeGranularity: uint32(readUint64(reader, "time granularity: ")),
	}
	if !readConfirm(reader, nil) {
		return
	}
	resp, err := c.PublishAsset(ctx, &connect.Request[hummingbird.PublishAssetRequest]{
		Msg: &hummingbird.PublishAssetRequest{
			Asset: asset,
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("Published asset ID %s\n", hex.EncodeToString(resp.Msg.AssetId))
}

func printUpdateAssetOptions() {
	fmt.Println("-> add")
	fmt.Println("-> remove")
	fmt.Println("-> list")
	fmt.Println("-> submit")
	fmt.Println("-> cancel")
}

func handleUpdate(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Handle update assets query")
	assetUpdates := make([]*hummingbird.AssetUpdate, 0, 1)
	for {
		fmt.Printf("Currently %d assets in update query.\n", len(assetUpdates))
		printUpdateAssetOptions()
		option, err := reader.ReadString('\n')
		option = strings.TrimSpace(option)
		if err != nil {
			fmt.Println(err)
			continue
		}
		switch {
		case option == "add":
			fmt.Println("-> delete")
			fmt.Println("-> update")
			subOption, err := reader.ReadString('\n')
			subOption = strings.TrimSpace(subOption)
			if err != nil {
				fmt.Println(err)
				continue
			}
			switch {
			case subOption == "delete":
				assetId, err := readAssetID(reader, "AssetID: ")
				if err != nil {
					fmt.Println(err)
					continue
				}
				assetUpdates = append(assetUpdates, &hummingbird.AssetUpdate{
					AssetId:   assetId,
					Operation: &hummingbird.AssetUpdate_Remove{},
				})
			case subOption == "update":
				assetId, err := readAssetID(reader, "AssetID: ")
				if err != nil {
					fmt.Println(err)
					continue
				}
				assetUpdates = append(assetUpdates, &hummingbird.AssetUpdate{
					AssetId: assetId,
					Operation: &hummingbird.AssetUpdate_Update{
						Update: &hummingbird.PublisherAsset{IfIdIngress: readOptionalUint32(reader, "ingress: "),
							IfIdEgress:      readOptionalUint32(reader, "egress: "),
							Bandwidth:       uint32(readUint64(reader, "bandwidth: ")),
							BandwidthMin:    uint32(readUint64(reader, "minimum bandwidth: ")),
							BandwidthMax:    uint32(readUint64(reader, "maximum bandwidth: ")),
							StartsAt:        timestamppb.New(readTime(reader, "Starts at (2026-06-23T13:25:36Z): ")),
							StopsAt:         timestamppb.New(readTime(reader, "Stops at (2026-06-23T13:25:36Z): ")),
							Price:           uint32(readUint64(reader, "price per kbit per second: ")),
							TimeMinDuration: uint32(readUint64(reader, "minimum time duration: ")),
							TimeMaxDuration: uint32(readUint64(reader, "maximum time duration: ")),
							TimeGranularity: uint32(readUint64(reader, "time granularity: "))},
					},
				})
			default:
				break
			}
		case option == "remove":
			index := readUint64(reader, "List index to remove: ")
			if int(index) >= len(assetUpdates) {
				fmt.Println("index invalid")
				break
			}
			assetUpdates = append(assetUpdates[:index], assetUpdates[index+1:]...)
		case option == "list":
			for index, asset := range assetUpdates {
				jsonAsset, _ := json.Marshal(asset.Operation)
				fmt.Printf("%d: asset_id: %s, operation: %s\n", index, hex.EncodeToString(asset.AssetId), string(jsonAsset))
			}
		case option == "submit":
			if !readConfirm(reader, nil) {
				continue
			}
			rep, err := c.UpdateAssets(ctx, &connect.Request[hummingbird.UpdateAssetsRequest]{
				Msg: &hummingbird.UpdateAssetsRequest{
					Assets: assetUpdates,
				},
			})
			if err != nil {
				fmt.Println(err)
				continue
			}
			for i, res := range rep.Msg.Result {
				if i >= len(assetUpdates) {
					break
				}
				switch t := res.ResultType.(type) {
				case *hummingbird.UpdateAssetResult_NewId:
					if t.NewId == nil {
						fmt.Printf("%s: deleted\n", hex.EncodeToString(assetUpdates[i].AssetId))
					} else {
						fmt.Printf("%s: updated to -> %s\n", hex.EncodeToString(assetUpdates[i].AssetId), hex.EncodeToString(t.NewId))
					}
				case *hummingbird.UpdateAssetResult_Error:
					fmt.Printf("%s: error: %s\n", hex.EncodeToString(assetUpdates[i].AssetId), t.Error)
				default:
					fmt.Printf("%s: unknown result", hex.EncodeToString(assetUpdates[i].AssetId))
				}
			}
			return
		case option == "cancel":
			return
		}
	}
}

func handleStatistics(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Handle statistics query")
	infoRep, err := c.Info(ctx, &connect.Request[hummingbird.MarketplaceInfoRequest]{})
	if err != nil {
		fmt.Println(err)
		return
	}
	startsAt := readTime(reader, "Starts at (2026-06-23T13:25:36Z): ").UTC().Truncate(time.Duration(infoRep.Msg.MaxStatisticsGranularity) * time.Second)
	stopsAt := hbird.RoundUpTime(readTime(reader, "Stops at (2026-06-23T13:25:36Z): ").UTC(), time.Duration(infoRep.Msg.MaxStatisticsGranularity)*time.Second)
	stepSize := readUint64(reader, "Step size: ")
	ingress := readOptionalUint32(reader, "Ingress: ")
	egress := readOptionalUint32(reader, "Egress: ")
	if !readConfirm(reader, nil) {
		return
	}
	resp, err := c.Statistics(ctx, &connect.Request[hummingbird.StatisticsRequest]{
		Msg: &hummingbird.StatisticsRequest{
			Start:       timestamppb.New(startsAt),
			End:         timestamppb.New(stopsAt),
			Step:        uint32(stepSize),
			IfIdIngress: ingress,
			IfIdEgress:  egress,
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	step := hbird.RoundUpDuration(time.Duration(stepSize)*time.Second, time.Duration(infoRep.Msg.MaxStatisticsGranularity)*time.Second)
	for i, stat := range resp.Msg.Statistics {
		intervalStart := startsAt.Add(time.Duration(i) * step)
		intervalEnd := intervalStart.Add(step)
		fmt.Printf("[%s - %s], Revenue: %d, Utilization %f\n", intervalStart.Format(time.RFC3339),
			intervalEnd.Format(time.RFC3339), stat.Revenue, stat.BandwidthUtilization)
	}
}
func handleSplit(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Handle split asset query.")
	assetId, err := readAssetID(reader, "AssetID: ")
	if err != nil {
		fmt.Println(err)
		return
	}
	splitOption := readOptionalString(reader, "spit using axis [bw,time]: ", nil)
	if splitOption == nil {
		fmt.Println("invalid split option.")
		return
	}
	req := &hummingbird.SplitAssetRequest{
		AssetId: assetId,
	}
	switch *splitOption {
	case "bw":
		bwSplits := readString(reader, "bw split (10,15,10): ")
		allSplits := strings.Split(bwSplits, ",")
		intSplits := make([]uint32, 0, len(allSplits))
		for _, split := range allSplits {
			intSplit, err := strconv.ParseUint(split, 10, 32)
			if err != nil {
				fmt.Println(err)
				return
			}
			intSplits = append(intSplits, uint32(intSplit))
		}
		req.SplitOption = &hummingbird.SplitAssetRequest_BwSplit{
			BwSplit: &hummingbird.SplitBwOption{
				Splits: intSplits,
			},
		}
	case "time":
		timeSplits := readString(reader, "time split (2026-06-23T13:25:36Z): ")
		allSplits := strings.Split(timeSplits, ",")
		timestampSplits := make([]*timestamppb.Timestamp, 0, len(allSplits))
		for _, split := range allSplits {
			timeSplit, err := time.Parse(time.RFC3339, split)
			if err != nil {
				fmt.Println(err)
				return
			}
			timestampSplits = append(timestampSplits, timestamppb.New(timeSplit))
		}
		req.SplitOption = &hummingbird.SplitAssetRequest_TimeSplit{
			TimeSplit: &hummingbird.SplitTimeOption{
				Splits: timestampSplits,
			},
		}
	default:
		fmt.Println("invalid split option.")
		return
	}
	if !readConfirm(reader, nil) {
		return
	}
	resp, err := c.SplitAsset(ctx, &connect.Request[hummingbird.SplitAssetRequest]{
		Msg: req,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("asset split into:")
	for _, assetId := range resp.Msg.AssetIds {
		fmt.Printf("- %s\n", hex.EncodeToString(assetId))
	}
}
func handleCombine(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Handle combine assets query.")
	assetIds := make([][]byte, 0, 2)
	for {
		assetId, err := readAssetID(reader, "AssetID (empty to continue): ")
		if err != nil {
			fmt.Println(err)
			return
		}
		if len(assetId) == 0 {
			break
		}
		assetIds = append(assetIds, assetId)
	}
	if !readConfirm(reader, nil) {
		return
	}
	resp, err := c.CombineAssets(ctx, &connect.Request[hummingbird.CombineAssetRequest]{
		Msg: &hummingbird.CombineAssetRequest{
			AssetIds: assetIds,
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("assets combined into %s\n", hex.EncodeToString(resp.Msg.AssetId))
}
func handleReservation(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	var ia *uint64
	var ingress *uint32
	var egress *uint32
	var bw *uint32
	var startsAt *time.Time
	var stopsAt *time.Time
	fmt.Println("Handle fetch reservation query. Filters are ignored if empty.")
	ia = readOptionalIAUint64(reader, "IA: ")
	ingress = readOptionalUint32(reader, "Ingress: ")
	egress = readOptionalUint32(reader, "Egress: ")
	bw = readOptionalUint32(reader, "BW: ")
	startsAt = readOptionalTime(reader, "Starts At (2006-01-02T15:04:05Z): ")
	stopsAt = readOptionalTime(reader, "Stops At (2006-01-02T15:04:05Z): ")

	req := &hummingbird.FetchReservationsRequest{
		Ia:        ia,
		IngressId: ingress,
		EgressId:  egress,
		Bandwidth: bw,
	}
	if startsAt != nil {
		req.StartsAt = timestamppb.New(*startsAt)
	}
	if stopsAt != nil {
		req.StopsAt = timestamppb.New(*stopsAt)
	}
	rep, err := c.FetchReservations(ctx, &connect.Request[hummingbird.FetchReservationsRequest]{
		Msg: req,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	type Reservation struct {
		ResId     uint32
		Ia        addr.IA
		IngressId uint32
		EgressId  uint32
		Bw        uint32
		EncodedBw uint16
		StartsAt  time.Time
		StopsAt   time.Time
		Ak        []byte
	}
	transformed := make([]*Reservation, 0, len(rep.Msg.Reservations))
	for _, res := range rep.Msg.Reservations {
		transformed = append(transformed, &Reservation{
			ResId:     res.ReservationId,
			Ia:        addr.IA(res.Ia),
			IngressId: res.IngressId,
			EgressId:  res.EgressId,
			Bw:        res.Bandwidth,
			EncodedBw: uint16(res.DataplaneEncoding),
			StartsAt:  res.StartsAt.AsTime(),
			StopsAt:   res.StopsAt.AsTime(),
			Ak:        res.AuthenticationKey,
		})
	}
	j, err := json.MarshalIndent(transformed, "", "\t")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(j))
}

func handleRedeem(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Redeem Query.")
	option := readOptionalUint32(reader, "Select option:\n0: ingress and egress assets\n1: interface-pair asset:\n")
	var rep *connect.Response[hummingbird.RedeemAssetResponse]
	if option == nil {
		return
	} else if *option == 0 {
		ingressAssetID, err := readAssetID(reader, "Ingress Asset ID: ")
		if err != nil {
			fmt.Println(err)
			return
		}
		egressAssetID, err := readAssetID(reader, "Egress Asset ID: ")
		if err != nil {
			fmt.Println(err)
			return
		}
		if !readConfirm(reader, nil) {
			return
		}
		rep, err = c.RedeemAsset(ctx, &connect.Request[hummingbird.RedeemAssetRequest]{
			Msg: &hummingbird.RedeemAssetRequest{
				Interfaces: &hummingbird.RedeemAssetRequest_Pair{
					Pair: &hummingbird.IngressEgressPair{
						IngressAssetId: ingressAssetID,
						EgressAssetId:  egressAssetID,
					},
				},
			},
		})
		if err != nil {
			fmt.Println(err)
			return
		}
	} else if *option == 1 {
		assetID, err := readAssetID(reader, "Interface-pair Asset ID: ")
		if err != nil {
			fmt.Println(err)
			return
		}
		if !readConfirm(reader, nil) {
			return
		}
		rep, err = c.RedeemAsset(ctx, &connect.Request[hummingbird.RedeemAssetRequest]{
			Msg: &hummingbird.RedeemAssetRequest{
				Interfaces: &hummingbird.RedeemAssetRequest_IfPairAssetId{
					IfPairAssetId: assetID,
				},
			},
		})
		if err != nil {
			fmt.Println(err)
			return
		}
	} else {
		return
	}
	fmt.Println("Redemption Result:")
	fmt.Printf("ResID: %d\nAk: %s\nBw: %d\nEncoding: %d\n", rep.Msg.ReservationId, rep.Msg.AuthenticationKey, rep.Msg.BandwidthRounded, rep.Msg.BwDataplaneEncoding)
}
func printBuyOptions() {
	fmt.Println("-> add")
	fmt.Println("-> remove")
	fmt.Println("-> list")
	fmt.Println("-> submit")
	fmt.Println("-> cancel")
}
func handleBuy(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	fmt.Println("Buy Query.")
	buyAssets := make([]*hummingbird.BuyAsset, 0, 1)
	for {
		fmt.Printf("Currently %d assets in the shopping cart.\n", len(buyAssets))
		printBuyOptions()
		option, err := reader.ReadString('\n')
		option = strings.TrimSpace(option)
		if err != nil {
			fmt.Println(err)
			continue
		}
		switch {
		case option == "add":
			assetId, err := readAssetID(reader, "AssetID: ")
			if err != nil {
				fmt.Println(err)
				return
			}
			fmt.Println("read asset id ", assetId, " with len ", len(assetId))
			buyAsset := &hummingbird.BuyAsset{
				AssetId:         assetId,
				StartsAtExactly: timestamppb.New(readTime(reader, "Starts at exactly (2026-06-23T13:25:36Z): ")),
				StopsAtExactly:  timestamppb.New(readTime(reader, "Stops at exactly (2026-06-23T13:25:36Z): ")),
				BandwidthExact:  uint32(readUint64(reader, "BW exact: ")),
			}
			buyAssets = append(buyAssets, buyAsset)
		case option == "remove":
			index := readUint64(reader, "List index to remove: ")
			if int(index) >= len(buyAssets) {
				fmt.Println("index invalid")
				break
			}
			buyAssets = append(buyAssets[:index], buyAssets[index+1:]...)
		case option == "list":
			for index, asset := range buyAssets {
				jsonAsset, _ := json.Marshal(asset)
				fmt.Printf("%d:%s\n", index, string(jsonAsset))
			}
		case option == "submit":
			maxPrice := readUint64(reader, "Max Price: ")
			if !readConfirm(reader, nil) {
				continue
			}
			rep, err := c.BuyAssets(ctx, &connect.Request[hummingbird.BuyAssetsRequest]{
				Msg: &hummingbird.BuyAssetsRequest{
					Assets:   buyAssets,
					MaxPrice: maxPrice,
				},
			})
			if err != nil {
				fmt.Println(err)
				continue
			}
			fmt.Printf("Bought assets for a total cost of %d\n", rep.Msg.Cost)
			for i, boughtAsset := range rep.Msg.Assets {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Print(hex.EncodeToString(boughtAsset.AssetId))
			}
			fmt.Print("\n")
			return
		case option == "cancel":
			return
		}
	}
}

func handleSearch(ctx context.Context, reader *bufio.Reader, c hummingbirdconnect.MarketplaceServiceClient) {
	var owned *bool
	var ia *uint64
	var ingress *uint32
	var egress *uint32
	var minReqBw *uint32
	var price *uint32
	var startsAtLatest *time.Time
	var stopsAtEarliest *time.Time
	fmt.Println("Search Query. Owned is mandatory, other filters are ignored if empty.")

	owned = readOptionalBool(reader, "Owned (true,false): ")
	ia = readOptionalIAUint64(reader, "IA: ")
	ingress = readOptionalUint32(reader, "Ingress: ")
	egress = readOptionalUint32(reader, "Egress: ")
	minReqBw = readOptionalUint32(reader, "Min Required BW: ")
	price = readOptionalUint32(reader, "Price: ")
	startsAtLatest = readOptionalTime(reader, "Starts At Latest (2006-01-02T15:04:05Z): ")
	stopsAtEarliest = readOptionalTime(reader, "Stops At Earliest (2006-01-02T15:04:05Z): ")
	if owned == nil {
		tmp := false
		owned = &tmp
	}
	msg := &hummingbird.SearchAssetsRequest{
		Owned:         *owned,
		Ia:            ia,
		IfIdIngress:   ingress,
		IfIdEgress:    egress,
		MinRequiredBw: minReqBw,
		Price:         price,
	}
	if startsAtLatest != nil {
		msg.StartsAtLatest = timestamppb.New(*startsAtLatest)
	}
	if stopsAtEarliest != nil {
		msg.StopsAtEarliest = timestamppb.New(*stopsAtEarliest)
	}
	for page := uint32(0); ; page++ {
		msg.Page = page
		rep, err := c.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
			Msg: msg,
		})
		if err != nil {
			fmt.Println(err)
			return
		}
		transformed := make([]Asset, 0, len(rep.Msg.Assets))
		for _, asset := range rep.Msg.Assets {
			transformed = append(transformed, Asset{
				ID:              hex.EncodeToString(asset.AssetId),
				IA:              addr.IA(asset.Ia),
				Bandwidth:       asset.Bandwidth,
				BandwidthMin:    asset.BandwidthMin,
				BandwidthMax:    asset.BandwidthMax,
				TimeMinDuration: asset.TimeMinDuration,
				TimeMaxDuration: asset.TimeMaxDuration,
				StartAt:         asset.StartsAt.AsTime(),
				StopsAt:         asset.StopsAt.AsTime(),
				Price:           asset.Price,
				IfIdIngress:     asset.IfIdIngress,
				IfIdEgress:      asset.IfIdEgress,
				TimeGranularity: asset.TimeGranularity,
			})
		}
		j, err := json.MarshalIndent(transformed, "", "\t")
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(string(j))
		prompt := "More assets? [yes, no]: "
		if !readConfirm(reader, &prompt) {
			return
		}
	}
}

func handleInfo(ctx context.Context, c hummingbirdconnect.MarketplaceServiceClient) {
	info, err := c.Info(ctx, &connect.Request[hummingbird.MarketplaceInfoRequest]{})
	if err != nil {
		fmt.Println(err)
		return
	}
	transform := func(base uint64) string {
		return fmt.Sprintf("%g%s", float64(base)*math.Pow10(-int(info.Msg.CurrencyExponent)), info.Msg.Currency)
	}
	fmt.Printf("Version: %d.%d\nCurrency: %g %s\nPricing strategy: %s\n",
		info.Msg.ApiMajorVersion, info.Msg.ApiMinorVersion, math.Pow10(-int(info.Msg.CurrencyExponent)),
		info.Msg.Currency, info.Msg.PricingStrategy)
	fmt.Printf("Transaction fees: %g%% + %s\nSplit or combine assets: %s\n",
		100*info.Msg.TransactionFeeRelative, transform(info.Msg.TransactionFeeAbsolute), transform(info.Msg.SplitCombineFeeAbsolute))
	if info.Msg.SupportsRedemptionDelegation {
		fmt.Printf("Redemption delegation hourly fee: %s\n", transform(info.Msg.DelegationHourlyFee))
	}
	fmt.Printf("Statistics granularity: %s\n", (time.Duration(info.Msg.MaxStatisticsGranularity) * time.Second).String())
}

func readConfirm(reader *bufio.Reader, prompt *string) bool {
	if prompt != nil {
		fmt.Print(*prompt)
	} else {
		fmt.Print("Confirm [yes,no]: ")
	}
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	text = strings.ToLower(text)
	if text == "yes" {
		return true
	}
	return false
}

func readAssetID(reader *bufio.Reader, prompt string) ([]byte, error) {
	str := readString(reader, prompt)
	return hex.DecodeString(str)
}

func readUint64(reader *bufio.Reader, prompt string) uint64 {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	v, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		fmt.Println("Invalid uint64")
		return 0
	}

	return v
}

func readOptionalIAUint64(reader *bufio.Reader, prompt string) *uint64 {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	if text == "" {
		return nil
	}
	ia, err := addr.ParseIA(text)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	v := uint64(ia)

	return &v
}

func readString(reader *bufio.Reader, prompt string) string {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	return text
}

func readOptionalString(reader *bufio.Reader, prompt string, defaultStr *string) *string {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	if text == "" {
		return defaultStr
	}

	return &text
}

func readOptionalUint32(reader *bufio.Reader, prompt string) *uint32 {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	if text == "" {
		return nil
	}

	v64, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		fmt.Println("Invalid uint32")
		return nil
	}

	v := uint32(v64)
	return &v
}

func readOptionalBool(reader *bufio.Reader, prompt string) *bool {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	if text == "" {
		return nil
	}

	v, err := strconv.ParseBool(text)
	if err != nil {
		fmt.Println("Invalid bool")
		return nil
	}

	return &v
}

func readTime(reader *bufio.Reader, prompt string) time.Time {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	if text == "" {
		return time.Time{}
	}

	// Example format: 2026-05-20T15:04:05
	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		fmt.Println("Invalid time format")
		return time.Time{}
	}

	return t
}

func readOptionalTime(reader *bufio.Reader, prompt string) *time.Time {
	fmt.Print(prompt)

	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)

	if text == "" {
		return nil
	}

	// Example format: 2026-05-20T15:04:05
	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		fmt.Println("Invalid time format")
		return nil
	}

	return &t
}

var insecure bool
var as_registration bool
var sciond string

func main() {
	flag.BoolVar(&insecure, "insecure", false, "indicates whether TLS insecure skip verify should be applied")
	flag.BoolVar(&as_registration, "register", false, "start AS registration")
	flag.StringVar(&sciond, "sciond", "", "address of the SCION daemon")
	flag.Parse()
	userInteraction()
}

type Asset struct {
	ID              string
	IA              addr.IA
	Bandwidth       uint32
	BandwidthMin    uint32
	BandwidthMax    uint32
	StartAt         time.Time
	StopsAt         time.Time
	Price           uint32
	TimeGranularity uint32
	TimeMinDuration uint32
	TimeMaxDuration uint32
	IfIdIngress     *uint32
	IfIdEgress      *uint32
}
