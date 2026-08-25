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
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"golang.org/x/crypto/bcrypt"
)

func AccountManagerInterceptor(verifier *TokenVerifier) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			authority := req.Header().Get(":authority")
			if authority == "" {
				authority = req.Header().Get("host")
			}

			ctx = context.WithValue(ctx, "authority", authority)
			authHeader := req.Header().Get("Authorization")
			method := req.Spec().Procedure
			fmt.Printf("AccountManagerInterceptor Interceptor for %s\n", method)
			requiredScope, found := methodScopes[method]
			if !found {
				//no rules apply
				fmt.Println("no rules apply for", method)
				return next(ctx, req)
			}
			if strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
				if tokenStr != "" {
					var err error
					ctx, err = verifier.contextFromJwt(ctx, tokenStr, requiredScope)
					if err != nil {
						return nil, err
					}
				}
			}
			return next(ctx, req)
		}
	}
}

func (s *Service) CreateChallenge(ctx context.Context, req *connect.Request[hummingbird.CreateChallengeRequest]) (*connect.Response[hummingbird.CreateChallengeResponse], error) {
	if s.registrationService == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, serrors.New("Registration not configured"))
	}
	challenge, err := s.registrationService.CreateChallenge(ctx, addr.IA(req.Msg.Ia))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.CreateChallengeResponse]{
		Msg: challenge,
	}, nil
}

func (s *Service) RegisterAS(ctx context.Context, req *connect.Request[hummingbird.RegisterASRequest]) (*connect.Response[hummingbird.RegisterASResponse], error) {
	if s.registrationService == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, serrors.New("Registration not configured"))
	}
	authority, ok := ctx.Value("authority").(string)
	if !ok || authority == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("Request must provide HTTP Host or HTTP2 :authority header"))
	}
	fmt.Println("Register AS", "Authority", authority)
	ia, err := s.registrationService.RegisterAS(ctx, req.Msg.Id, req.Msg.SignedChallenge, authority)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	dbUser, err := s.store.GetASUser(ctx, ia)
	if err != nil {
		fmt.Println(err)

		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if dbUser == nil {
		dbUser = &db.DBASUser{
			IA:           ia,
			TokenVersion: 0,
		}
		_, err = s.store.CreateASUser(ctx, dbUser)
		if err != nil {
			fmt.Println(err)
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	now := time.Now()
	publisherToken, err := s.signer.GenerateToken(jwt.MapClaims{
		"sub":   ia.String(),
		"scope": registration.ScopeAssetPublisher,
		"exp":   now.Add(time.Hour * 24 * 7).Unix(),
		"iat":   now.Unix(),
		"ver":   dbUser.TokenVersion,
	})
	if err != nil {
		fmt.Println(err)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	redemptionToken, err := s.signer.GenerateToken(jwt.MapClaims{
		"sub":   ia.String(),
		"scope": registration.ScopeRedemptionService,
		"exp":   now.Add(time.Hour * 24 * 7).Unix(),
		"iat":   now.Unix(),
		"ver":   dbUser.TokenVersion,
	})
	if err != nil {
		fmt.Println(err)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.RegisterASResponse]{
		Msg: &hummingbird.RegisterASResponse{
			JwtPublisher:  publisherToken,
			JwtRedemption: redemptionToken,
		},
	}, nil
}

func (s *Service) ResetJWT(ctx context.Context, req *connect.Request[hummingbird.JWTResetRequest]) (*connect.Response[hummingbird.JWTResetResponse], error) {
	ver, ok := ctx.Value("ver").(int64)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, serrors.New("invalid token"))
	}
	v := ctx.Value("user")
	switch x := v.(type) {
	case nil:
		return nil, connect.NewError(connect.CodeUnauthenticated, serrors.New("invalid token"))
	case int64:
		_, err := s.store.IncrementUserJWTVersion(ctx, x, ver)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
	case addr.IA:
		_, err := s.store.IncrementASJWTVersion(ctx, x, ver)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		kickRedemptionService := func() {
			handler := s.FindRedemptionServerHandler(x)
			handler.remoteConnectionOpenChannel <- nil
			_ = <-handler.remoteConnectionCloseChannel
		}
		kickRedemptionService()
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid token"))
	}
	return &connect.Response[hummingbird.JWTResetResponse]{Msg: &hummingbird.JWTResetResponse{}}, nil
}

func (s *Service) SetPassword(ctx context.Context, req *connect.Request[hummingbird.SetPasswordRequest]) (*connect.Response[hummingbird.SetPasswordResponse], error) {
	ia, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, serrors.New("invalid token"))
	}
	hash, err := bcrypt.GenerateFromPassword(
		[]byte(req.Msg.Password),
		12,
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid token"))
	}
	_, err = s.store.SetASAuthenticationToken(ctx, ia, string(hash))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.SetPasswordResponse]{
		Msg: &hummingbird.SetPasswordResponse{},
	}, nil
}
