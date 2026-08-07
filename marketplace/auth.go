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

package marketplace

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
)

var methodScopes = map[string]string{
	"/proto.hummingbird.v1.MarketplaceService/PublishAsset":      "AssetPublisher",
	"/proto.hummingbird.v1.MarketplaceService/UpdateAssets":      "AssetPublisher",
	"/proto.hummingbird.v1.MarketplaceService/Statistics":        "AssetPublisher",
	"/proto.hummingbird.v1.MarketplaceService/SearchAssets":      "User,AssetPublisher",
	"/proto.hummingbird.v1.MarketplaceService/SplitAsset":        "User",
	"/proto.hummingbird.v1.MarketplaceService/CombineAssets":     "User",
	"/proto.hummingbird.v1.MarketplaceService/BuyAssets":         "User",
	"/proto.hummingbird.v1.MarketplaceService/FetchReservations": "User",
	"/proto.hummingbird.v1.MarketplaceService/RedeemAsset":       "User",
	"/proto.hummingbird.v1.RedemptionService/RedeemASAsset":      "RedemptionService",
	"/proto.hummingbird.v1.RedemptionService/DelegateRedemption": "RedemptionService",

	"/proto.hummingbird.v1.AccountService/ResetJWT":    "User,AssetPublisher,RedemptionService",
	"/proto.hummingbird.v1.AccountService/SetPassword": "AssetPublisher,RedemptionService",
}

type AuthInterceptor struct {
	TokenVerifier *TokenVerifier
}

func NewAuthInterceptor(v *TokenVerifier) *AuthInterceptor {
	return &AuthInterceptor{
		TokenVerifier: v,
	}
}

type TokenVerifier struct {
	Store       *storage.MarketplaceStorage
	JWTVerifier *registration.Verifier
}

func (a *TokenVerifier) verifyTokenVersion(
	ctx context.Context,
	user any,
	claims jwt.MapClaims,
	scopes map[string]bool,
) (int64, error) {
	tokenVersion := int64(0)
	if scopes["AssetPublisher"] || scopes["RedemptionService"] {
		dbUser, err := a.Store.GetASUser(ctx, user.(addr.IA))
		if err != nil {
			return 0, connect.NewError(
				connect.CodeUnauthenticated,
				fmt.Errorf("invalid token"),
			)
		}
		tokenVersion = dbUser.TokenVersion
	} else if scopes["User"] {
		dbAccount, err := a.Store.GetAccountByAccountID(ctx, user.(int64))
		if err != nil {
			return 0, connect.NewError(
				connect.CodeUnauthenticated,
				fmt.Errorf("invalid token"),
			)
		}
		tokenVersion = dbAccount.TokenVersion
	} else {
		return 0, connect.NewError(
			connect.CodeUnauthenticated,
			fmt.Errorf("invalid token"),
		)
	}
	versionClaim, ok := claims["ver"].(float64)
	if !ok || int64(versionClaim) < tokenVersion {
		return 0, connect.NewError(
			connect.CodeUnauthenticated,
			fmt.Errorf("invalid token"),
		)
	}
	return int64(versionClaim), nil
}

func (a *AuthInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

func (t *TokenVerifier) contextFromJwt(
	ctx context.Context,
	tokenStr string,
	requiredScope string,
) (context.Context, error) {
	token, err := t.JWTVerifier.VerifyToken(tokenStr)
	if err != nil || !token.Valid {
		fmt.Println("invalid token")
		return nil, connect.NewError(
			connect.CodeUnauthenticated,
			fmt.Errorf("invalid token"),
		)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, connect.NewError(
			connect.CodeUnauthenticated,
			fmt.Errorf("invalid claims"),
		)
	}

	user, ok := claims["sub"].(string)
	if !ok || user == "" {
		return nil, connect.NewError(
			connect.CodeUnauthenticated,
			fmt.Errorf("missing subject"),
		)
	}
	scopeStr, ok := claims["scope"].(string)
	if !ok || scopeStr == "" {
		return nil, connect.NewError(
			connect.CodePermissionDenied,
			fmt.Errorf("missing scopes"),
		)
	}
	scopes := parseScopes(scopeStr)
	if requiredScope != "" {
		found := false
		for _, r := range strings.Split(requiredScope, ",") {
			if scopes[r] {
				found = true
				break
			}
		}
		if !found {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("missing scope: %s", requiredScope))
		}
	}
	if scopes["User"] {
		userid, err := strconv.ParseInt(user, 10, 64)
		if err != nil {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("invalid token"))
		}
		tokenVer, err := t.verifyTokenVersion(ctx, userid, claims, scopes)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, "ver", tokenVer)
		ctx = context.WithValue(ctx, "user", userid)
	} else {
		ia, err := addr.ParseIA(user)
		if err != nil {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("invalid scope"))
		}
		tokenVer, err := t.verifyTokenVersion(ctx, ia, claims, scopes)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, "ver", tokenVer)
		ctx = context.WithValue(ctx, "user", ia)
	}

	return ctx, nil
}

func (a *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(
		ctx context.Context,
		req connect.AnyRequest,
	) (connect.AnyResponse, error) {

		method := req.Spec().Procedure
		fmt.Printf("Auth Interceptor for %s\n", method)
		requiredScope, found := methodScopes[method]
		if !found {
			//no rules apply
			fmt.Println("no rules apply for", method)
			return next(ctx, req)
		}
		authHeader := req.Header().Get("Authorization")

		if !strings.HasPrefix(authHeader, "Bearer ") {
			return nil, connect.NewError(
				connect.CodeUnauthenticated,
				fmt.Errorf("missing bearer token"),
			)
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		ctx, err := a.TokenVerifier.contextFromJwt(ctx, tokenStr, requiredScope)
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *AuthInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {

	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		method := conn.Spec().Procedure
		fmt.Printf("Auth Interceptor for %s\n", method)
		requiredScope, found := methodScopes[method]
		if !found {
			//no rules apply
			fmt.Println("no rules apply for", method)
			return next(ctx, conn)
		}
		authHeader := conn.RequestHeader().Get("Authorization")

		if !strings.HasPrefix(authHeader, "Bearer ") {
			return connect.NewError(
				connect.CodeUnauthenticated,
				fmt.Errorf("missing bearer token"),
			)
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		ctx, err := a.TokenVerifier.contextFromJwt(ctx, tokenStr, requiredScope)
		if err != nil {
			return err
		}

		return next(ctx, conn)
	}
}

func parseScopes(scopeStr string) map[string]bool {
	scopes := make(map[string]bool)
	for s := range strings.SplitSeq(scopeStr, ",") {
		if s != "" {
			scopes[s] = true
		}
	}
	return scopes
}
