package auth

import (
	"context"
	"fmt"
)

// This file provides 2 implementations of Authenticator interface. Each implementation has multiple
// authenticators with the same issuer. These implementations are needed when
// multiple microservices use the same OIDC server (i.e. the same issuer) with different
// client IDs. ID token contains the client ID in the audience field. The standard says the following about
// the audience field in the ID token:
//		https://openid.net/specs/openid-connect-core-1_0.html
//		This specifies how the audience field is used for the id_token.
//		aud
//		REQUIRED. Audience(s) that this ID Token is intended for. It MUST contain the OAuth 2.0 client_id of
//		the Relying Party as an audience value. It MAY also contain identifiers for other audiences. In the general case,
//		the aud value is an array of case sensitive strings. In the common special case when there is one audience,
//		the aud value MAY be a single case sensitive string.
//
// Note that the audience value can be an array of strings in a general case. There are 2 authenticator implementations
// provided:
//
// 1> multiAuthenticatorByClientID assumes that the audience is a single string containing client_id which is used
// to extract the matching authenticator from the map and only that authenticator is used to authenticate the token.
// Use this implementation when the audience is expected to contain only the client_id.
//
// 2> iteratingMultiAuthenticator allows the audience value to be an array of strings. It delegates the interpretation
// of audience to the specified athenticators by iterating over them. All authenticators use the same issuer and
// at most one of the authenticators shall successfully authenticate the token. Others will reject the token
// because the client_id will not match.

type multiAuthenticatorByClientID struct {
	issuer         string
	authenticators map[string]Authenticator
}

// NewMultiAuthenticatorByClientID returns Authenticator implementation that assumes that the audience
// field in the token contains just the client ID, which is also the key in the authenticators map
// passed to this function. All authenticators must use the same issuer.
func NewMultiAuthenticatorByClientID(
	issuer string,
	authenticatorsByClientID map[string]Authenticator,
) (Authenticator, error) {
	if len(authenticatorsByClientID) == 0 {
		return nil, fmt.Errorf("empty authenticators for issuer %s", issuer)
	}
	return &multiAuthenticatorByClientID{
		issuer:         issuer,
		authenticators: authenticatorsByClientID,
	}, nil
}

func (m *multiAuthenticatorByClientID) AuthenticateToken(ctx context.Context, idToken string) (*Claims, error) {
	tokenClaims, err := TokenClaims(idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get claims from token: %w", err)
	}
	if tokenClaims.Issuer != m.issuer {
		return nil, fmt.Errorf("token was issued by %s which does not match issuer %s of this multi authenticator",
			tokenClaims.Issuer, m.issuer)
	}
	var clientID string
	aud, err := tokenClaims.GetAudience()
	if err != nil {
		return nil, fmt.Errorf("failed to find audience in the claims: %w", err)
	}
	// This implementation assumes that the audience is a single string containing the client ID.
	// If that is not true, the "iterating" multi authenticator should be used instead.
	if len(aud) != 1 {
		return nil, fmt.Errorf("unable to determine client ID;"+
			" expected a single value in the audience field of the token but found %d: %v", len(aud), aud)
	}
	clientID = aud[0]
	if clientID == "" {
		return nil, fmt.Errorf("empty client ID in the audience claim")
	}
	authenticator, ok := m.authenticators[clientID]
	if !ok {
		return nil, fmt.Errorf("failed to find authenticator for issuer %s and client ID %s",
			tokenClaims.Issuer, clientID)
	}
	claims, err := authenticator.AuthenticateToken(ctx, idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate token for issuer %s and client ID %s: %w",
			tokenClaims.Issuer, clientID, err)
	}
	return claims, nil
}

type iteratingMultiAuthenticator struct {
	issuer         string
	authenticators []Authenticator
}

// NewIteratingMultiAuthenticator returns Authenticator implementation that iterates over all
// the supplied authenticators to authenticate a token. All authenticators must use the same issuer.
func NewIteratingMultiAuthenticator(
	issuer string,
	authenticators []Authenticator,
) (Authenticator, error) {
	if len(authenticators) == 0 {
		return nil, fmt.Errorf("empty authenticators for issuer %s", issuer)
	}
	return &iteratingMultiAuthenticator{
		issuer:         issuer,
		authenticators: authenticators,
	}, nil
}

func (m *iteratingMultiAuthenticator) AuthenticateToken(ctx context.Context, idToken string) (*Claims, error) {
	tokenClaims, err := TokenClaims(idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get claims from token: %w", err)
	}
	if tokenClaims.Issuer != m.issuer {
		return nil, fmt.Errorf("token was issued by %s which does not match issuer %s of this multi authenticator",
			tokenClaims.Issuer, m.issuer)
	}
	for _, authenticator := range m.authenticators {
		claims, err := authenticator.AuthenticateToken(ctx, idToken)
		if err == nil {
			return claims, nil
		}
	}
	return nil, fmt.Errorf("failed to authenticate token for issuer %s", tokenClaims.Issuer)
}
